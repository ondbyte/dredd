# dredd-test/cobol:latest
#
# GnuCOBOL on a Debian base. The Debian-packaged compiler is invoked as
# `cobc` and produces standalone executables.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends gnucobol \
 && rm -rf /var/lib/apt/lists/*
