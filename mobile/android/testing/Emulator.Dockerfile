FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends libx11-6 libnss3 libpulse0 libxcomposite1 libxcursor1 libxi6 libxrandr2 libxtst6 libasound2 libglu1-mesa ca-certificates && rm -rf /var/lib/apt/lists/*
ARG EMULATOR_UID=1000
ARG KVM_GID=992
RUN groupadd -g "$KVM_GID" kvm && useradd -m -u "$EMULATOR_UID" -g "$KVM_GID" -d /home/emulator emulator
ENV HOME=/home/emulator
USER emulator
