#!/usr/bin/env python3
"""Add a bounded synthetic session to the local legacy poll (standard library)."""
import concurrent.futures
import json
import re
import time
import urllib.request

BASE = "http://127.0.0.1:8080"


def request(path, method="GET"):
    with urllib.request.urlopen(
        urllib.request.Request(BASE + path, method=method), timeout=5
    ) as response:
        return response.read().decode()


def stream_event(path, expected):
    with urllib.request.urlopen(BASE + path, timeout=10) as response:
        for line in response:
            if expected in line.decode():
                return True
    raise AssertionError("Stream ended without expected event: " + path)


def main():
    page = request("/")
    initial = json.loads(re.search(r"data-signals='([^']+)'", page).group(1))
    added = {"A": 0, "B": 0}
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        streams = [
            pool.submit(stream_event, "/stream", "event: datastar-merge-signals"),
            pool.submit(stream_event, "/metrics/feed", "event:point"),
        ]
        # Alternating leaders and pauses produce several real snapshot points.
        for round_number, choices in enumerate(["AAAAB", "BBBBBB", "AAABB", "AAAAAB", "BBBBBA"]):
            if round_number:
                request("/")
            for choice in choices:
                response = request("/click/" + choice, "POST")
                added[choice] += 1
                expected = initial["counter" + choice] + added[choice]
                assert '"counter%s":%d' % (choice, expected) in response, response
                time.sleep(0.2)
            time.sleep(2)
        for stream in streams:
            assert stream.result()

    expected = {"clicksA": initial["counterA"] + added["A"],
                "clicksB": initial["counterB"] + added["B"]}
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        history = json.loads(request("/metrics/history")) or []
        if history and all(history[-1][key] == value for key, value in expected.items()):
            print(json.dumps({"added_clicks": added, "added_page_views": 5,
                              "persisted_totals": expected, "history_points": len(history),
                              "live_streams_verified": True}, indent=2))
            return
        time.sleep(1)
    raise AssertionError("Final clicks were not persisted; check snapshot configuration")


if __name__ == "__main__":
    main()
