"""The persisted queries of infra/bff-mobile in a cell (its BM02): what the mobile entry's build would list. prepare.py
writes them as the member's BFF_PERSISTED_QUERIES; the L3 scenarios (scenarios/test_bff.py) send them by their
SHA-256. One query per page of the first batch."""
from __future__ import annotations

import hashlib
import json
import pathlib

QUERIES = {
    "home": """query Home($taskType: String, $cursor: String, $limit: Int) {
  myTasks(taskType: $taskType, cursor: $cursor, limit: $limit) {
    items { id component taskType state version stale action { component path } summary { title } }
    nextCursor
  }
  myNotifications(limit: 5) { unread items { id category title } }
}""",
    "task": """query Task($component: String!, $id: ID!) {
  task(component: $component, id: $id) {
    id component state version action { component path } summary { title fields { key label value } }
  }
}""",
    "products": """query Products($q: String, $cursor: String, $limit: Int) {
  products(q: $q, cursor: $cursor, limit: $limit) { items { id code name baseUnit masked } nextCursor }
}""",
    "product": """query Product($id: ID!) {
  product(id: $id) { id code name stock { warehouse { id code name } onHand reserved available } }
}""",
    "customers": """query Customers($le: ID, $q: String, $cursor: String, $limit: Int) {
  customers(legalEntityId: $le, q: $q, cursor: $cursor, limit: $limit) { items { id name state masked } nextCursor }
}""",
    "orders": """query Orders($state: String, $cursor: String, $limit: Int) {
  myOrders(state: $state, cursor: $cursor, limit: $limit) {
    items { id number state currency total customer { partnerId name } masked } nextCursor
  }
}""",
    "opportunities": """query Opportunities($stage: String, $cursor: String, $limit: Int) {
  myOpportunities(stage: $stage, cursor: $cursor, limit: $limit) { items { id name stage customer { name } } nextCursor }
}""",
}


def hash_of(name: str) -> str:
    return hashlib.sha256(QUERIES[name].encode()).hexdigest()


def write(cell_dir: pathlib.Path) -> str:
    """Writes the list under the cell's .secrets/bff/ and returns its path relative to the cell (a file:// value)."""
    d = cell_dir / ".secrets" / "bff"
    d.mkdir(parents=True, exist_ok=True)
    path = d / "persisted-queries.json"
    # one line: it travels as one environment variable (BrickKit escapes $ and newlines; one line needs neither)
    path.write_text(json.dumps({hash_of(n): q for n, q in QUERIES.items()}))
    path.chmod(0o644)
    return str(path.relative_to(cell_dir))
