package main

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

type LoopDetector struct {
	callFingerprints []string
	iterFingerprints []string
	threshold        int
	windowSize       int
}

func NewLoopDetector(threshold int) *LoopDetector {
	if threshold <= 0 {
		threshold = 3
	}
	windowSize := threshold * 4
	if windowSize < 20 {
		windowSize = 20
	}
	return &LoopDetector{
		callFingerprints: make([]string, 0, windowSize),
		iterFingerprints: make([]string, 0, windowSize),
		threshold:        threshold,
		windowSize:       windowSize,
	}
}

func (d *LoopDetector) Enabled() bool {
	return d.threshold > 0
}

func (d *LoopDetector) fingerprint(name, args string) string {
	h := sha256.Sum256([]byte(name + "\x00" + args))
	return hex.EncodeToString(h[:8])
}

func (d *LoopDetector) consecutiveCount(fingerprints []string, fp string) int {
	count := 0
	for i := len(fingerprints) - 1; i >= 0; i-- {
		if fingerprints[i] == fp {
			count++
		} else {
			break
		}
	}
	return count
}

func (d *LoopDetector) addCall(fp string) {
	d.callFingerprints = append(d.callFingerprints, fp)
	if len(d.callFingerprints) > d.windowSize {
		d.callFingerprints = d.callFingerprints[len(d.callFingerprints)-d.windowSize:]
	}
}

func (d *LoopDetector) addIter(fp string) {
	d.iterFingerprints = append(d.iterFingerprints, fp)
	if len(d.iterFingerprints) > d.windowSize {
		d.iterFingerprints = d.iterFingerprints[len(d.iterFingerprints)-d.windowSize:]
	}
}

func (d *LoopDetector) CheckToolCall(name, args string) (detected bool, count int) {
	fp := d.fingerprint(name, args)
	count = d.consecutiveCount(d.callFingerprints, fp) + 1
	d.addCall(fp)
	detected = count >= d.threshold
	return
}

func (d *LoopDetector) CheckIteration(toolCalls []openai.ToolCall) (detected bool, count int) {
	if len(toolCalls) == 0 {
		return false, 0
	}

	type callSig struct {
		name string
		args string
	}
	sigs := make([]callSig, len(toolCalls))
	for i, tc := range toolCalls {
		sigs[i] = callSig{tc.Function.Name, tc.Function.Arguments}
	}
	sort.Slice(sigs, func(i, j int) bool {
		if sigs[i].name != sigs[j].name {
			return sigs[i].name < sigs[j].name
		}
		return sigs[i].args < sigs[j].args
	})

	parts := make([]string, len(sigs))
	for i, s := range sigs {
		parts[i] = s.name + "\x00" + s.args
	}
	fp := d.fingerprint("_iter_", strings.Join(parts, "\x01"))

	count = d.consecutiveCount(d.iterFingerprints, fp) + 1
	d.addIter(fp)
	detected = count >= d.threshold
	return
}

func (d *LoopDetector) Reset() {
	d.callFingerprints = d.callFingerprints[:0]
	d.iterFingerprints = d.iterFingerprints[:0]
}
