"""L3: business chains across components, against a running cell (BE_ACC_CELL, its seed data in; see README.md)."""
from __future__ import annotations

import os
import pathlib
import sys
import time
import uuid
from dataclasses import dataclass, field

import httpx
import pytest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from cells.spec import Cell  # noqa: E402
from harness import need  # noqa: E402


class Refused(AssertionError):
    pass


@dataclass
class User:
    """A test account's calls to the cell's components by ID, through their host ports."""

    name: str
    cell: Cell
    token: str
    principal: str
    http: httpx.Client = field(default_factory=lambda: httpx.Client(timeout=30))

    def url(self, component: str, path: str) -> str:
        return f"http://127.0.0.1:{self.cell.host_port(component)}{path}"

    def call(self, method: str, component: str, path: str, body=None, idempotent: bool = True) -> httpx.Response:
        headers = {"Authorization": f"Bearer {self.token}"}
        if method != "GET" and idempotent:
            headers["Idempotency-Key"] = f"l3:{uuid.uuid4()}"
        return self.http.request(method, self.url(component, path), json=body, headers=headers)

    def get(self, component: str, path: str) -> dict:
        return self.ok(self.call("GET", component, path))

    def post(self, component: str, path: str, body=None) -> dict:
        return self.ok(self.call("POST", component, path, body))

    @staticmethod
    def ok(r: httpx.Response) -> dict:
        if r.status_code != 200:
            raise Refused(f"{r.request.method} {r.request.url.path}: {r.status_code} {r.text[:300]}")
        return r.json()


def eventually(check, seconds: float = 30, every: float = 0.5):
    """check() until it returns a truthy value; the last AssertionError when time runs out."""
    deadline, last = time.monotonic() + seconds, None
    while time.monotonic() < deadline:
        try:
            out = check()
            if out:
                return out
        except AssertionError as e:
            last = e
        time.sleep(every)
    raise last or AssertionError(f"not true within {seconds} s")


@pytest.fixture(scope="session")
def cell() -> Cell:
    return Cell.load(need("BE_ACC_CELL"))


@pytest.fixture(scope="session")
def login(cell):
    password = need("BE_ACC_IAM_TEST_PASSWORD")
    users: dict[str, User] = {}

    def get(name: str) -> User:
        if name not in users:
            r = httpx.post(f"http://127.0.0.1:{cell.host_port('infra/iam-casdoor')}/api/iam/dev/token",
                           json={"username": name, "password": password}, timeout=30)
            assert r.status_code == 200, f"{name} cannot log in: {r.status_code} {r.text[:200]}"
            users[name] = User(name, cell, r.json()["access_token"], r.json()["principal"])
        return users[name]

    return get


@pytest.fixture(scope="session")
def bob(login) -> User:
    return login("test.bob")


@dataclass
class World:
    """The seed data the scenarios start from (docs/zh/03-seed-data.md): CN01, its Shanghai and Beijing warehouses, a
    product in stock in Shanghai, a service, a customer under a 1,000,000 limit and one under 20,000 (CNY)."""

    le: str
    shanghai: str
    beijing: str
    goods: dict
    tin: dict
    service: dict
    large: str
    small: str
    departments: dict


@pytest.fixture(scope="session")
def world(bob) -> World:
    le = next(e["id"] for e in bob.get("mdm/org", "/api/org/legal-entities?limit=200")["legal_entities"]
              if e["code"] == "CN01")
    whs = {w["code"]: w["id"] for w in bob.get("erp/inventory", f"/api/inventory/warehouses?limit=200&legal_entity_id={le}")["warehouses"]}
    priced = {i["product_id"] for pl in bob.get("erp/sales", f"/api/sales/price-lists?legal_entity_id={le}")["price_lists"]
              if pl["currency"] == "CNY"
              for i in bob.get("erp/sales", f"/api/sales/price-lists/{pl['id']}/items?on=2026-10-01&limit=200")["items"]}
    products, cursor = {}, ""
    while True:
        page = bob.get("mdm/product", f"/api/product/products?limit=200&cursor={cursor}")
        products.update({p["code"]: p for p in page["products"]})
        if not (cursor := page.get("next_cursor") or ""):
            break
    stocked = {p["product_id"]: p for p in bob.get(
        "erp/inventory", f"/api/inventory/positions?limit=200&available_only=true&warehouse_id={whs['SH-01']}")["positions"]}
    goods = next(p for p in products.values() if p["id"] in stocked and p["id"] in priced and p["tracking"] == "none"
                 and float(stocked[p["id"]]["available"]) >= 20)
    service = next(p for p in products.values() if p["kind"] == "service" and p["id"] in priced)
    limits = [lim for lim in bob.get("erp/credit", f"/api/credit/limits?limit=200&legal_entity_id={le}&currency=CNY&state=active")["limits"]]
    large = next(lim["partner_id"] for lim in limits if lim["limit"] == "1000000")
    small = next(lim["partner_id"] for lim in limits if lim["limit"] == "20000")
    units = {u["code"]: u["id"] for u in bob.get("mdm/org", f"/api/org/units?limit=200&legal_entity_id={le}")["units"]}
    return World(le, whs["SH-01"], whs["BJ-01"], goods, products["TEA-00-TIN"], service, large, small, units)


def pytest_configure(config):
    os.environ.setdefault("NO_PROXY", "127.0.0.1")


@pytest.fixture(scope="session")
def second_goods(bob, world) -> dict:
    """Another product in stock in Shanghai, priced and untracked, for orders with two goods (RS16)."""
    le = world.le
    priced = {i["product_id"] for pl in bob.get("erp/sales", f"/api/sales/price-lists?legal_entity_id={le}")["price_lists"]
              if pl["currency"] == "CNY"
              for i in bob.get("erp/sales", f"/api/sales/price-lists/{pl['id']}/items?on=2026-10-01&limit=200")["items"]}
    stocked = bob.get("erp/inventory", f"/api/inventory/positions?limit=200&available_only=true&warehouse_id={world.shanghai}")["positions"]
    for p in stocked:
        if p["product_id"] != world.goods["id"] and p["product_id"] in priced and float(p["available"]) >= 50:
            product = bob.get("mdm/product", f"/api/product/products/{p['product_id']}")
            if product["tracking"] == "none":
                return product
    raise AssertionError("no second untracked product in stock in Shanghai: seed the cell first")
