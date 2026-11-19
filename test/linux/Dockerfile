FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl procps python3 strace && rm -rf /var/lib/apt/lists/*
# Run as root: Docker/Colima kernels don't support unprivileged user namespace
# creation via Go's SysProcAttr.UidMappings. Root still exercises the full
# sandbox codepath (namespace + seccomp + deny mounts + write isolation).
COPY wt /usr/local/bin/wt
COPY wt /root/wt-fixture
ENV WT_TEST_BINARY=/root/wt-fixture
COPY wt-preview /root/wt-preview-fixture
ENV WT_TEST_PREVIEW_BINARY=/root/wt-preview-fixture
# The hosted-shape relay test runs this CGO_ENABLED=0 build; no Go toolchain here.
ENV WT_TEST_NOCGO_BINARY=/root/wt-fixture
# The compiled run-inspect CLI tests consume this read-only source fixture
# relative to /root, just as ordinary go test consumes it from cmd/wt.
COPY runinspect-orphan /internal/runinspect/testdata/orphan
# Real checksum installer used by the compiled onboarding PTY fixture.
COPY install.sh /scripts/install.sh
# Repo files the compiled cmd/wt tests read; wt-tests.sh runs them from /cmd/wt.
COPY repo-fixtures/ /
COPY mock-agent /usr/local/bin/claude
COPY run-tests /root/run-tests
COPY sandbox-tests /root/sandbox-tests
COPY wt-tests /root/wt-tests
COPY eggclient-tests /root/eggclient-tests
COPY roost-pin-tests.sh /root/roost-pin-tests.sh
COPY wt-tests.sh /root/wt-tests.sh
RUN chmod +x /usr/local/bin/wt /usr/local/bin/claude /root/run-tests /root/sandbox-tests /root/wt-tests /root/eggclient-tests
RUN mkdir -p /root/.wingthing/eggs /root/.wingthing/logs /root/.claude /root/.ssh
# Non-root user for namespace failure tests
RUN useradd -m -u 1000 testuser && \
    mkdir -p /home/testuser/.wingthing/eggs /home/testuser/.wingthing/logs /home/testuser/.claude && \
    chown -R testuser:testuser /home/testuser
WORKDIR /root
