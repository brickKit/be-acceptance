"""L3, attachments (C9): erp/purchase owns its orders' attachments and asks infra/attachment for them on the user's
behalf (assembly protocol 12, D68); the user uploads to the presigned address on the object store and confirms at
infra/attachment, which copies the upload to a final key and checks what it reads there. A normal category (the
contract) downloads from a presigned address; a strict one (the invoice scan) by a one-time credential presented at
infra/attachment, whose response only saves. The seed's buyer test.buyer.cn reads the orders of CN01's PUR but not
their invoice scans; the lead does. Each test makes its own order on top of the seed data."""
from __future__ import annotations

import httpx
import pytest

from conftest import eventually
from test_purchase import supplier  # noqa: F401  (the fixture)

PDF = (b"%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n"
       b"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 595 842]>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")
HTML = b"<!DOCTYPE html><html><body><script>alert(1)</script></body></html>\n"


@pytest.fixture(autouse=True)
def _needs_attachments(cell):
    for c in ("infra/attachment", "erp/purchase"):
        if c not in cell.placed:
            pytest.skip(f"the cell has no {c}")


def draft_order(bob, world, supplier) -> dict:  # noqa: F811
    return bob.post("erp/purchase", "/api/purchase/orders", {
        "legal_entity_id": world.le, "supplier_id": supplier, "currency": "CNY", "owner_id": bob.principal,
        "department_id": world.departments["PUR"],
        "lines": [{"product_id": world.goods["id"], "quantity": "1", "unit": world.goods["base_unit"],
                   "unit_price": "100", "warehouse_id": world.shanghai}]})


def session(user, order: dict, category: str, body: bytes, name: str = "file.pdf", declared: str = "application/pdf"):
    return user.post("erp/purchase", f"/api/purchase/orders/{order['id']}/attachments",
                     {"category": category, "file_name": name, "size": len(body), "content_type": declared})


def put(up: dict, body: bytes, declared: str = "application/pdf") -> int:
    return httpx.put(up["upload_url"], content=body, headers={"Content-Type": declared}, timeout=30).status_code


def confirm(user, up: dict) -> httpx.Response:
    return user.call("POST", "infra/attachment", up["confirm"]["path"], {"credential": up["confirm_credential"]})


def attach(user, order, category, body=PDF, name="file.pdf") -> str:
    up = session(user, order, category, body, name)
    assert put(up, body) // 100 == 2
    r = confirm(user, up)
    assert r.status_code == 200, r.text
    assert r.json()["state"] == "available", r.json()
    return up["attachment_id"]


def listed(user, order) -> dict:
    return {a["id"]: a for a in user.get("erp/purchase", f"/api/purchase/orders/{order['id']}/attachments")["attachments"]}


def test_attachment_a_contract_round_trip(bob, world, supplier):  # noqa: F811
    """AT03, AT04, AT06: the confirmation before the upload answers UPLOAD_INCOMPLETE and keeps the credential; after
    it the attachment is available with the size and type infra/attachment read itself; the contract downloads from a
    presigned address as the same bytes, saved as a file."""
    o = draft_order(bob, world, supplier)
    up = session(bob, o, "erp.purchase.order_contract", PDF, "合同.pdf")
    early = confirm(bob, up)
    assert early.status_code == 409 and early.json()["reason"] == "UPLOAD_INCOMPLETE", early.text
    assert put(up, PDF) // 100 == 2
    r = confirm(bob, up)
    assert r.status_code == 200 and r.json()["state"] == "available", r.text
    again = confirm(bob, up)
    assert again.status_code != 200, "a confirm credential is used once"
    a = listed(bob, o)[up["attachment_id"]]
    assert (a["size"], a["content_type"], a["file_name"]) == (len(PDF), "application/pdf", "合同.pdf")
    dl = bob.post("erp/purchase", f"/api/purchase/orders/{o['id']}/attachments/{a['id']}/download")
    got = httpx.get(dl["url"], timeout=30)
    assert got.status_code == 200 and got.content == PDF
    assert got.headers.get("content-disposition", "").startswith("attachment"), got.headers


def test_attachment_the_content_decides_not_the_claim(bob, world, supplier):  # noqa: F811
    """AT04: an HTML page declared as a PDF is rejected by what infra/attachment detects in the final object."""
    o = draft_order(bob, world, supplier)
    up = session(bob, o, "erp.purchase.order_contract", HTML, "not-a.pdf")
    assert put(up, HTML) // 100 == 2
    r = confirm(bob, up)
    assert r.status_code == 200 and r.json()["state"] == "rejected", r.text
    dl = bob.call("POST", "erp/purchase", f"/api/purchase/orders/{o['id']}/attachments/{up['attachment_id']}/download")
    assert dl.status_code != 200, "a rejected attachment never downloads"


def test_attachment_the_invoice_scans_are_strict(bob, login, world, supplier):  # noqa: F811
    """AT06, security 09a: the invoice scan is not listed for the buyer who lacks its field, nor downloadable; the lead
    gets a one-time credential and infra/attachment answers the bytes with headers that only save them."""
    buyer, lead = login("test.buyer.cn"), login("test.buyer.lead")
    o = draft_order(bob, world, supplier)
    contract = attach(bob, o, "erp.purchase.order_contract")
    invoice = attach(bob, o, "erp.purchase.order_invoice", name="发票.pdf")
    assert set(eventually(lambda: listed(buyer, o) or None)) == {contract}
    assert set(listed(lead, o)) == {contract, invoice}
    no = buyer.call("POST", "erp/purchase", f"/api/purchase/orders/{o['id']}/attachments/{invoice}/download")
    assert no.status_code == 404, no.text
    dl = lead.post("erp/purchase", f"/api/purchase/orders/{o['id']}/attachments/{invoice}/download")
    assert "url" not in dl and dl["download"]["path"] == "/api/attachment/downloads"
    got = lead.call("POST", "infra/attachment", dl["download"]["path"], {"credential": dl["credential"]})
    assert got.status_code == 200 and got.content == PDF, got.text[:200]
    assert got.headers.get("x-content-type-options") == "nosniff"
    assert "sandbox" in got.headers.get("content-security-policy", "")
    assert got.headers.get("content-disposition", "").startswith("attachment")
    twice = lead.call("POST", "infra/attachment", dl["download"]["path"], {"credential": dl["credential"]})
    assert twice.status_code != 200, "a download credential is used once"
    stolen = lead.post("erp/purchase", f"/api/purchase/orders/{o['id']}/attachments/{invoice}/download")
    other = buyer.call("POST", "infra/attachment", stolen["download"]["path"], {"credential": stolen["credential"]})
    assert other.status_code != 200, "a credential is the user's it was issued to"


def test_attachment_the_order_ends(bob, world, supplier):  # noqa: F811
    """D68: a removed attachment is detached; the order's cancellation detaches the rest (erp/purchase commands it by
    a work item after the cancellation commits) and the cancelled order takes no new attachment."""
    o = draft_order(bob, world, supplier)
    first = attach(bob, o, "erp.purchase.order_contract")
    second = attach(bob, o, "erp.purchase.order_contract", name="附录.pdf")
    r = bob.call("DELETE", "erp/purchase", f"/api/purchase/orders/{o['id']}/attachments/{first}")
    assert r.status_code == 204, r.text
    assert listed(bob, o)[first]["state"] == "detached"
    assert bob.post("erp/purchase", f"/api/purchase/orders/{o['id']}/cancel")["state"] == "cancelled"
    eventually(lambda: listed(bob, o)[second]["state"] == "detached", seconds=60)
    late = bob.call("POST", "erp/purchase", f"/api/purchase/orders/{o['id']}/attachments",
                    {"category": "erp.purchase.order_contract", "file_name": "x.pdf", "size": len(PDF),
                     "content_type": "application/pdf"})
    assert late.status_code == 400, late.text
