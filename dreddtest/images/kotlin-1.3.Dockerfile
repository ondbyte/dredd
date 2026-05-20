# dredd-test/kotlin-1.3:latest
#
# Kotlin compiler 1.3.x layered onto an OpenJDK 13 base. No public Kotlin
# 1.3 image exists on Docker Hub anymore, so we vendor the compiler tarball
# from kotlinlang.org.
FROM eclipse-temurin:11-jdk
ARG KOTLIN_VERSION=1.3.72
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl unzip \
 && curl -fsSL "https://github.com/JetBrains/kotlin/releases/download/v${KOTLIN_VERSION}/kotlin-compiler-${KOTLIN_VERSION}.zip" \
      -o /tmp/kotlinc.zip \
 && unzip -q /tmp/kotlinc.zip -d /opt \
 && ln -s /opt/kotlinc/bin/kotlinc /usr/local/bin/kotlinc \
 && ln -s /opt/kotlinc/bin/kotlin  /usr/local/bin/kotlin \
 && rm /tmp/kotlinc.zip \
 && rm -rf /var/lib/apt/lists/*
