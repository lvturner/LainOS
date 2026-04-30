Name:           lain
Version:        %{getenv:LAIN_VERSION}
Release:        1%{?dist}
Summary:        Lain AI agent shell
License:        Proprietary

Source0:        lain

%description
Terminal-based AI agent shell with MCP support, TUI interface, and
session management.

%install
install -Dm 0755 %{SOURCE0} %{buildroot}/usr/bin/lain

%files
/usr/bin/lain
