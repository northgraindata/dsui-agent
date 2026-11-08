# Tunnel protocol, version 1

This document is the contract between `dsui-agent` and a DSUI server.
Both sides must follow it exactly.
Reference implementations: `internal/protocol` and `internal/tunnel` in this repository (agent), and the connector tunnel in DSUI (server).

## Transport

The agent opens one WebSocket to `wss://<instance>/api/v1/connectors/tunnel`.
Plain `ws://` is allowed only for local development.
The agent authenticates with `Authorization: Bearer <token>` on the upgrade request.
It also sends `X-DSUI-Protocol: 1` and `User-Agent: dsui-agent/<version>`.

The server answers the upgrade with:

- `401` or `403` when the token is unknown, revoked, or the connector is disabled.
- `404` when the server has no connector support.
- `101` when accepted.

A server must accept binary messages of at least 64 KiB (the largest frame is 5 bytes of header plus 32 KiB of payload).
Servers and proxies in front of them must keep idle WebSockets open for at least 60 seconds.
The agent sends a WebSocket ping every 20 seconds and a heartbeat every 30 seconds.

## Control messages (text messages, JSON)

All control messages are JSON objects with a `type` field.
Unknown fields must be ignored.

### hello (agent to server)

The first message after the upgrade.

```json
{
  "type": "hello",
  "protocol": 1,
  "version": "0.3.0",
  "name": "datacenter-1",
  "hostname": "bastion-01",
  "os": "linux",
  "arch": "amd64",
  "localAddrs": ["10.20.1.5"],
  "targets": ["airflow.internal:8080", "10.20.0.0/16:5432"]
}
```

`targets` is the agent's allowlist exactly as configured.
DSUI shows it in the UI and uses it to offer target suggestions.
It is informational: the agent enforces the list itself.

### ready (server to agent)

The reply to a valid hello.

```json
{ "type": "ready", "observedIp": "203.0.113.7", "maxStreams": 128 }
```

`observedIp` is the address the server saw the connection come from.
`maxStreams` is optional and lowers the agent's own stream limit when smaller.

### error (server to agent)

Sent instead of ready, or at any time before closing the socket.

```json
{ "type": "error", "code": "protocol", "message": "unsupported protocol version 2" }
```

Codes: `unauthorized`, `protocol`.

### heartbeat (agent to server)

```json
{ "type": "heartbeat", "targets": ["airflow.internal:8080"], "activeStreams": 3 }
```

The server records the time of the last heartbeat.
A connector is online while its WebSocket is open and a heartbeat arrived in the last 90 seconds.

## Frames (binary messages)

Each binary message is one frame.

| Bytes | Meaning |
| --- | --- |
| 0 | frame type |
| 1 to 4 | stream id, big endian, never 0 |
| 5 and up | payload |

Stream ids are allocated by the server, starting at 1 and increasing.
An id is never reused within one WebSocket connection.

| Type | Name | Direction | Payload |
| --- | --- | --- | --- |
| 1 | OPEN | server to agent | `host:port`, UTF-8 |
| 2 | OPEN_OK | agent to server | none |
| 3 | OPEN_ERR | agent to server | JSON `{"code": "...", "message": "..."}` |
| 4 | DATA | both | up to 32 KiB of stream bytes |
| 5 | WINDOW | both | uint32 big endian, bytes of new credit |
| 6 | CLOSE | both | none, the sender writes no more data |
| 7 | RESET | both | none, abort the stream |

### Opening a stream

1. The server sends `OPEN` with the target address.
2. The agent checks its allowlist, dials TCP, and answers `OPEN_OK` or `OPEN_ERR`.
3. The server may send `DATA` right after `OPEN` without waiting for `OPEN_OK`.
   The agent queues it.

`OPEN_ERR` codes: `not_allowed`, `unreachable`, `timeout`, `too_many_streams`, `bad_target`.

### Flow control

Each direction of each stream has a credit window.
Both sides start with 262144 bytes (256 KiB) of credit to send.
A sender must not send more `DATA` bytes than it has credit.
A receiver grants more credit with `WINDOW` after it has handed the bytes to its socket.
The agent grants in batches of at least 64 KiB.
A peer that overruns the window gets `RESET` for that stream.

Credit is per stream, so one slow stream never blocks the others.

### Closing

`CLOSE` is a half close, like `shutdown(SHUT_WR)`.
The agent reaches EOF on the target and sends `CLOSE`.
The server sends `CLOSE` when the client side finished writing.
A stream ends when both sides sent `CLOSE`, or when either side sends `RESET`.
The agent sends `RESET` when the target connection fails.

When the WebSocket closes, all streams end.
The agent reconnects with exponential backoff from 1 to 60 seconds, with jitter.

## What the server must do

- Allocate stream ids and enforce `maxStreams`.
- Honor the credit window in both directions.
- Treat the agent's allowlist as authoritative: never retry a target after `not_allowed`.
- Attribute every stream to the connector and workspace that owns the token, and log it.
- Never accept one token on two simultaneous WebSockets without closing the older one.
- Keep tokens hashed at rest, and compare in constant time.

## Versioning

`protocol` in `hello` is the version the agent speaks.
The server answers `error` with code `protocol` when it cannot serve that version.
Backward compatible additions (new optional JSON fields, new `OPEN_ERR` codes) do not change the version.
Changing frame layout or semantics increments it.
