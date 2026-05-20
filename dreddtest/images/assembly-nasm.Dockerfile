# dredd-test/assembly-nasm:latest
#
# Provides nasm + binutils (ld) on a small Debian base. Used by the
# all-languages Docker test for the Assembly (NASM) case.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends nasm binutils libc6-dev \
 && rm -rf /var/lib/apt/lists/*
