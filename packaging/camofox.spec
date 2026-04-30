Name:           camofox
Version:        %{getenv:CAMOFOX_VERSION}
Release:        1%{?dist}
Summary:        Anti-detection headless browser server
License:        Proprietary
AutoReqProv:    no
Requires:       nodejs

Source0:        camofox.tar.gz
Source1:        camofox.service

%description
Camofox is a Camoufox-based anti-detection browser server providing a REST
API for AI agents on port 9377.

%install
mkdir -p %{buildroot}/usr/lib/camofox
tar -xzf %{SOURCE0} -C %{buildroot}/usr/lib/camofox --strip-components=1
install -Dm 0644 %{SOURCE1} %{buildroot}/usr/lib/systemd/user/camofox.service

%files
/usr/lib/camofox
/usr/lib/systemd/user/camofox.service
