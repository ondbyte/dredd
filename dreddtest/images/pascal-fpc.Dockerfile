# dredd-test/pascal-fpc:latest
#
# Free Pascal Compiler on a Debian base.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends fpc \
 && rm -rf /var/lib/apt/lists/*
