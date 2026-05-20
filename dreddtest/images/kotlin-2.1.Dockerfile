# dredd-test/kotlin-2.1:latest
#
# Kotlin compiler 2.1.x layered onto a Temurin JDK 17 base. No public
# Kotlin Docker image exists, so we vendor the compiler tarball from
# kotlinlang.org.
FROM eclipse-temurin:17-jdk
ARG KOTLIN_VERSION=2.1.0
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl unzip \
 && curl -fsSL "https://github.com/JetBrains/kotlin/releases/download/v${KOTLIN_VERSION}/kotlin-compiler-${KOTLIN_VERSION}.zip" \
      -o /tmp/kotlinc.zip \
 && unzip -q /tmp/kotlinc.zip -d /opt \
 && ln -s /opt/kotlinc/bin/kotlinc /usr/local/bin/kotlinc \
 && ln -s /opt/kotlinc/bin/kotlin  /usr/local/bin/kotlin \
 && rm /tmp/kotlinc.zip \
 && rm -rf /var/lib/apt/lists/*
