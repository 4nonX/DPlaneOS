#!/usr/bin/env python3
"""A TCP proxy for failure tests: a slow or partitioned link on one runner.

    netproxy.py LISTEN_PORT TARGET_HOST:TARGET_PORT CONTROL_FILE

CONTROL_FILE holds the link's state, read for every chunk, so a test changes
it at any time without restarting the proxy:

    ok              forward as is
    delay <ms>      forward every chunk after <ms> milliseconds (a slow WAN)
    drop            accept connections, forward nothing, answer nothing
                    (like packets dropped by a firewall: callers hit their
                    timeouts, not a quick "connection refused")

A missing or unreadable file means ok.
"""
import asyncio
import sys


def state(path):
    try:
        with open(path) as f:
            parts = f.read().split()
    except OSError:
        return "ok", 0
    if not parts:
        return "ok", 0
    if parts[0] == "delay" and len(parts) > 1:
        return "delay", int(parts[1]) / 1000
    return parts[0], 0


async def pipe(reader, writer, control):
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            mode, delay = state(control)
            while mode == "drop":  # hold the data: nothing gets through
                await asyncio.sleep(0.5)
                mode, delay = state(control)
            if delay:
                await asyncio.sleep(delay)
            writer.write(data)
            await writer.drain()
    except (ConnectionError, asyncio.CancelledError):
        pass
    finally:
        try:
            writer.close()
        except Exception:
            pass


async def handle(client_r, client_w, target, control):
    host, port = target.rsplit(":", 1)
    while state(control)[0] == "drop":  # the connection hangs, as behind a dropping firewall
        await asyncio.sleep(0.5)
    try:
        server_r, server_w = await asyncio.open_connection(host, int(port))
    except OSError:
        client_w.close()
        return
    await asyncio.gather(pipe(client_r, server_w, control), pipe(server_r, client_w, control))


async def main():
    listen, target, control = int(sys.argv[1]), sys.argv[2], sys.argv[3]
    srv = await asyncio.start_server(lambda r, w: handle(r, w, target, control), "127.0.0.1", listen)
    async with srv:
        await srv.serve_forever()


if __name__ == "__main__":
    asyncio.run(main())
