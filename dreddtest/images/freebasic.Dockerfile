# dredd-test/freebasic:latest
#
# Provides the FreeBASIC compiler (fbc) on a Debian base. FreeBASIC is not
# packaged in current Debian releases, so we install from the upstream
# binary tarball. The fbc 1.10.x binary is linked against libtinfo.so.5
# which bookworm no longer ships, so we pull libtinfo5 from the Debian
# bullseye archive.
FROM debian:bookworm-slim
ARG FBC_VERSION=1.10.1
ARG LIBTINFO5_DEB_URL=http://archive.debian.org/debian/pool/main/n/ncurses/libtinfo5_6.2+20201114-2+deb11u2_amd64.deb
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates curl xz-utils \
        gcc libc6-dev libncurses-dev libtinfo-dev \
        libffi-dev libgl-dev libx11-dev libxext-dev libxrender-dev \
        libxrandr-dev libxpm-dev \
 && curl -fsSL "${LIBTINFO5_DEB_URL}" -o /tmp/libtinfo5.deb \
 && dpkg -i /tmp/libtinfo5.deb \
 && rm /tmp/libtinfo5.deb \
 && curl -fsSL "https://downloads.sourceforge.net/project/fbc/FreeBASIC-${FBC_VERSION}/Binaries-Linux/FreeBASIC-${FBC_VERSION}-linux-x86_64.tar.xz" \
      -o /tmp/fbc.tar.xz \
 && mkdir -p /opt/freebasic \
 && tar -xJf /tmp/fbc.tar.xz -C /opt/freebasic --strip-components=1 \
 && ln -sf /opt/freebasic/bin/fbc /usr/local/bin/fbc \
 && rm /tmp/fbc.tar.xz \
 && rm -rf /var/lib/apt/lists/*
