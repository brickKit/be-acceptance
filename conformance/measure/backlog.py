"""The bus backlog (experiment plan, "积压"): for every JetStream consumer of the given event streams, the messages
not yet delivered, those delivered but not acknowledged, and the age of the oldest message not acknowledged."""
from __future__ import annotations

import datetime as dt

import nats
from nats.js.errors import NotFoundError

from besdk.events.core import jetstream_name


async def jetstream(url: str, streams: list[str]) -> dict[str, dict[str, dict]]:
    nc = await nats.connect(url)
    js = nc.jetstream()
    out: dict[str, dict[str, dict]] = {}
    try:
        for stream in streams:
            name = jetstream_name(stream)
            try:
                consumers = await js.consumers_info(name)
            except NotFoundError:
                continue
            out[stream] = {}
            for c in consumers:
                age = 0.0
                if c.num_pending or c.num_ack_pending:
                    try:
                        m = await js.get_msg(name, c.ack_floor.stream_seq + 1)
                        when = dt.datetime.fromisoformat(m.time) if isinstance(m.time, str) else m.time
                        age = (dt.datetime.now(dt.timezone.utc) - when).total_seconds() if when else 0.0
                    except NotFoundError:
                        pass
                out[stream][c.name] = {"pending": c.num_pending, "ack_pending": c.num_ack_pending, "oldest_age": age}
    finally:
        await nc.close()
    return out
