# dredd-test/objective-c:latest
#
# Provides gcc with the GNU Objective-C runtime on a Debian base. Required
# because Linux's stock gcc image doesn't ship the objc runtime headers.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        gcc gobjc libobjc-12-dev libc6-dev \
 && rm -rf /var/lib/apt/lists/*
