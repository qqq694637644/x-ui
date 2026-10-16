# RSX1 overseas exit agent via frp XTCP

The Go implementation lives in `reverseexit/` and is invoked by
`x-ui reverse-socks-exit` or the standalone `cmd/reverse-socks-exit` binary.
No web control/UI code is changed.

It **actively connects** from the overseas VPS to the existing frpc
XTCP **Visitor** local TCP bind. The other side remains the mainland NAT
node's frpc XTCP **Proxy**, forwarding to Python 3.13 `nat_node` RSX1
multiplexer. The Python node exposes a local SOCKS5 TCP CONNECT and UDP
ASSOCIATE entrypoint; this Go agent performs the actual Internet egress
TCP dials and UDP sends/receives. Neither WARP+ nor NAT traversal is changed.

Setup, both frpc TOML snippets, the RSX1 protocol and the cross-language
integration tests are documented in
[nat_node](https://github.com/qqq694637644/nat_node).

Build and run:

```sh
go build -o x-ui .
export REVERSE_SOCKS_TOKEN='same-private-token-as-nat-node'
./x-ui reverse-socks-exit -visitor 127.0.0.1:6000 -retry 2s
```

Or build only the exit agent, without the x-ui panel dependencies:

```sh
go build -o reverse-socks-exit ./cmd/reverse-socks-exit
REVERSE_SOCKS_TOKEN='same-private-token-as-nat-node' ./reverse-socks-exit -visitor 127.0.0.1:6000
```

The token must be at least 16 characters and is read **only from an
environment variable**, never a command-line argument. By default the
frpc visitor binds to localhost. Authenticate/encrypt the underlying
XTCP transport as appropriate. This release uses a single reliable
byte stream for TCP and UDP and does not attempt packet-level UDP hole
punching or a system-wide VPN.
