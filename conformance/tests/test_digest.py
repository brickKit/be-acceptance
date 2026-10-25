"""The step 8 result digest's comparison (cells/digest.py): two cells after the same input."""
from cells.digest import compare


def test_equal_digests_have_no_differences():
    d = {"erp_sales.sales_order": {"confirmed": 3, "draft": 1}, "infra_audit.audit_record": {"count": 9}}
    assert compare(d, d) == []


def test_every_item_that_differs_or_is_missing_is_listed():
    a = {"erp_sales.sales_order": {"confirmed": 3, "draft": 1}, "erp_billing.invoice": {"issued": 2}}
    b = {"erp_sales.sales_order": {"confirmed": 2, "draft": 1, "cancelled": 1}}
    assert compare(a, b) == [
        "erp_billing.invoice: only in the first",
        "erp_sales.sales_order.cancelled: None != 1",
        "erp_sales.sales_order.confirmed: 3 != 2",
    ]
