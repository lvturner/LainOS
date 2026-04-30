Name:           wh-gateway
Version:        %{getenv:WH_GATEWAY_VERSION}
Release:        1%{?dist}
Summary:        Webhook HTTP gateway
License:        Proprietary

Source0:        wh-gateway
Source1:        wh-gateway.service

%description
Routes incoming webhooks to user-defined commands. Config is YAML with
hot-reload via fsnotify.

%install
install -Dm 0755 %{SOURCE0} %{buildroot}/usr/bin/wh-gateway
install -Dm 0644 %{SOURCE1} %{buildroot}/usr/lib/systemd/user/wh-gateway.service

%files
/usr/bin/wh-gateway
/usr/lib/systemd/user/wh-gateway.service
