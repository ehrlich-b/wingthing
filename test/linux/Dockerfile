FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl procps python3 strace && rm -rf /var/lib/apt/lists/*
# Run as root: Docker/Colima kernels don't support unprivileged user namespace
# creation via Go's SysProcAttr.UidMappings. Root still exercises the full
# sandbox codepath (namespace + seccomp + deny mounts + write isolation).
COPY wt /usr/local/bin/wt
COPY mock-agent /usr/local/bin/claude
COPY run-tests /root/run-tests
COPY sandbox-tests /root/sandbox-tests
COPY wt-tests /root/wt-tests
RUN chmod +x /usr/local/bin/wt /usr/local/bin/claude /root/run-tests /root/sandbox-tests /root/wt-tests
RUN mkdir -p /root/.wingthing/eggs /root/.wingthing/logs /root/.claude /root/.ssh
# Non-root user for namespace failure tests
RUN useradd -m -u 1000 testuser && \
    mkdir -p /home/testuser/.wingthing/eggs /home/testuser/.wingthing/logs /home/testuser/.claude && \
    chown -R testuser:testuser /home/testuser
WORKDIR /root
