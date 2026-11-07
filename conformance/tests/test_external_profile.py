"""The external commands profile (foundations item 4, T9b; be-protocol chapter 13; the assembly project's protocol 10)
against each probe's payment and the suite's fake payment system (external/fake.py): a result the caller cannot know
is queried before anything is sent again, a reference the fake deduplicates is sent again only within its window, a
payment is never doubled, and an irrevocable payment is sent only after its issue record reached the evidence store.
Each probe declares three capabilities: no dedupe, a 24-hour dedupe, irrevocable (the test deployment's evidence store
down, then up). The Go probe has a variant for each (go, go2, go3); the Python probe has two, so the first (py) is
started again irrevocable for the last part (its payment since 3.2.48, foundations item 6 T8)."""
import asyncio

import pytest

from external.fake import CUT_RECORDED, CUT_UNRECORDED, LIMIT, REJECT, Fake, serve
from harness import Deployment, fresh_bus

AMOUNT = {"amount": "100.00", "currency": "EUR"}


async def state_of(p, ref: str, want: str | None = None, within: float = 20.0) -> dict:
    got = {}
    for _ in range(int(within / 0.2)):
        r = await p.get(f"/api/probe/payment/{ref}")
        if r.status_code == 200:
            got = r.json()
            if want is None or got["state"] == want:
                return got
        await asyncio.sleep(0.2)
    raise AssertionError(f"{p.spec.id} {ref}: {got} is not {want}")


async def pay(p, ref: str) -> None:
    r = await p.post("/api/probe/payment", {"reference": ref, "params": AMOUNT})
    assert r.status_code == 200, r.text


@pytest.mark.parametrize("kind", ["go", "py"])
async def test_the_probe_conforms_to_the_external_commands_profile(kind, specs, world, tmp_path):
    fake = Fake()
    server, url = serve(fake)
    first, second = specs[kind], specs[kind + "2"]
    third = specs["go3"] if kind == "go" else first  # the Python probe's first variant again, irrevocable
    d = Deployment(world, await fresh_bus("jetstream", list({first.stream, second.stream, third.stream})), tmp_path)
    try:
        common = {"PROBE_EXTERNAL_URL": url, "PROBE_EXTERNAL_IRREVOCABLE": "false"}
        plain = await d.start(first, await d.env(first, common))
        dedupe = await d.start(second, await d.env(second, {**common, "PROBE_EXTERNAL_DEDUPE_WINDOW": "24h"}))

        # a payment: sent once, final
        await pay(plain, "r-ok")
        got = await state_of(plain, "r-ok", "succeeded")
        assert fake.taken("r-ok") == 1 and got["external_id"]
        await pay(plain, "r-ok")  # the same request again (P13.2): the same command, nothing sent again
        await asyncio.sleep(3)
        assert fake.taken("r-ok") == 1

        # EI01 + found: the answer cut after the payment: unknown, queried, final; never paid twice
        fake.faults = [CUT_RECORDED]
        await pay(plain, "r-cut-paid")
        got = await state_of(plain, "r-cut-paid", "succeeded")
        assert fake.taken("r-cut-paid") == 1, "queried, not sent again"
        assert [a["kind"] for a in got["attempts"]] == ["send", "query"]

        # EI03: cut before the payment, no dedupe: "not found" may still land: manual, not sent again
        fake.faults = [CUT_UNRECORDED]
        await pay(plain, "r-cut-lost")
        got = await state_of(plain, "r-cut-lost", "manual")
        assert fake.taken("r-cut-lost") == 0
        assert [a["kind"] for a in got["attempts"]] == ["send", "query"]

        # EI04: the same with dedupe declared (and the fake deduplicating): sent again with the same reference
        fake.dedupe, fake.faults = True, [CUT_UNRECORDED]
        await pay(dedupe, "r-dedupe")
        got = await state_of(dedupe, "r-dedupe", "succeeded")
        assert fake.taken("r-dedupe") == 1
        assert [a["kind"] for a in got["attempts"]] == ["send", "query", "send"]
        assert got["resend_deadline"], "P13.7: written after a send that may have reached"
        fake.dedupe = False

        # refused for good; explicitly not taken now (backoff)
        fake.faults = [REJECT]
        await pay(plain, "r-rejected")
        await state_of(plain, "r-rejected", "rejected")
        fake.faults = [LIMIT]
        await pay(plain, "r-limited")
        await state_of(plain, "r-limited", "retry_wait")
        assert fake.taken("r-rejected") == fake.taken("r-limited") == 0

        # EI02: the query down: unknown stays unknown
        fake.query_down, fake.faults = True, [CUT_UNRECORDED]
        await pay(plain, "r-query-down")
        await state_of(plain, "r-query-down", "unknown")
        await asyncio.sleep(3)
        assert (await state_of(plain, "r-query-down"))["state"] == "unknown"
        fake.query_down = False

        # P13.4: irrevocable, the evidence store down and no alert channel: the issue record is taken nowhere, the
        # payment is never sent; with the store back, the issue record first, then the payment
        if third is first:
            await plain.stop()
        world.evidence_down = True  # the test deployment's evidence store (besdk.testing, be-sdk-python a57)
        irrevocable = await d.start(third, await d.env(third, {"PROBE_EXTERNAL_URL": url}))
        await pay(irrevocable, "r-irrevocable")
        got = await state_of(irrevocable, "r-irrevocable", "retry_wait")
        assert fake.taken("r-irrevocable") == 0
        assert "issue record" in got["reason"]
        world.evidence_down = False
        await pay(irrevocable, "r-irrevocable-2")
        await state_of(irrevocable, "r-irrevocable-2", "succeeded")
        assert fake.taken("r-irrevocable-2") == 1
        issued = [k for k in world.evidence if k.endswith("/intent")]
        assert issued, "the issue record reached the evidence store before the payment"
    finally:
        server.shutdown()
        await d.stop()
