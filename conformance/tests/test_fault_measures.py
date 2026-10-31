"""Suite C's measures around a stopped bus (cells/faults.py): the outbox backlog's peak and how long it took to drain."""
from cells.faults import backlog_summary


def test_peak_and_drain_against_the_backlog_before_the_stop():
    # unix second, unpublished rows; the bus stops at 100 and starts at 103; before the stop a row or two is in flight
    samples = [(97, 1), (98, 0), (99, 2), (100, 9), (101, 30), (102, 55), (103, 60), (104, 20), (105, 3), (106, 1)]
    assert backlog_summary(samples, stopped=100, started=103) == {"before": 2, "peak": 60, "peak_at": 3.0,
                                                                  "drained_after": 3.0}


def test_a_backlog_that_never_drains_says_so():
    assert backlog_summary([(99, 0), (100, 5), (101, 9), (102, 9)], stopped=100, started=101)["drained_after"] is None
