FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends libx11-6 libnss3 libpulse0 libxcomposite1 libxcursor1 libxi6 libxrandr2 libxtst6 libasound2 libglu1-mesa ca-certificates && rm -rf /var/lib/apt/lists/*
RUN groupadd -g 992 kvm && useradd -u 1000 -g 992 -d /work/lanes/wt-android/sdk/emulator-home ehrlich
USER ehrlich
