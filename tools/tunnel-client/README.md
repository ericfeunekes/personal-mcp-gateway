# OpenAI Secure MCP Tunnel client

The tunnel client is a local runtime dependency, not source code for this
repository. Download the appropriate client from OpenAI's Secure MCP Tunnel
setup flow, verify the published checksum, and place the executable at:

```text
tools/tunnel-client/tunnel-client
```

The executable is intentionally ignored by Git. Do not commit downloaded
archives, generated profiles, runtime keys, tunnel identifiers, or health URL
files.
