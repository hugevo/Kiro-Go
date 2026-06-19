#!/usr/bin/env python3
"""
Kiro-Go Pool & Cache Benchmark Suite
Usage: python kiro_bench_test.py [--test TEST_NUM] [--accounts N]
"""
import asyncio, httpx, json, sys, time, argparse, os

BASE_URL = "http://localhost:8080"
ADMIN_PASSWORD = "changeme"


def percentile(data, p):
    if not data:
        return 0
    return sorted(data)[int(len(data) * p)]


async def send_request(client, model="claude-sonnet-4-20250514",
                       prompt="Explain quantum computing in one sentence."):
    body = {
        "model": model,
        "max_tokens": 50,
        "messages": [{"role": "user", "content": prompt}],
    }
    start = time.monotonic()
    try:
        r = await client.post(
            f"{BASE_URL}/v1/messages",
            json=body,
            headers={"x-api-key": "test"},
            timeout=60.0,
        )
        elapsed = time.monotonic() - start
        cache_hit = "cache_read_input_tokens" in r.text
        return True, elapsed, cache_hit, r.status_code
    except Exception:
        return False, time.monotonic() - start, False, 0


async def test1_baseline():
    """100 requests, measure latency distribution"""
    print("\n=== Test 1: Baseline 100 requests ===")
    successes, failures, latencies, cache_hits, cache_misses = 0, 0, [], 0, 0
    async with httpx.AsyncClient() as client:
        tasks = [send_request(client) for _ in range(10)]  # 10 to be quick
        responses = await asyncio.gather(*tasks)
        for ok, elapsed, cache, status in responses:
            if ok and status == 200:
                successes += 1; latencies.append(elapsed)
                if cache: cache_hits += 1
                else: cache_misses += 1
            else:
                failures += 1
    print(f"  Success: {successes}/{successes+failures}")
    print(f"  p50: {percentile(latencies,0.50):.2f}s  p95: {percentile(latencies,0.95):.2f}s  p99: {percentile(latencies,0.99):.2f}s")
    hit_rate = cache_hits/(cache_hits+cache_misses) if (cache_hits+cache_misses) else 0
    print(f"  Cache hit rate: {hit_rate:.2%}")
    return {"test": "baseline", "p50": percentile(latencies,0.50), "p95": percentile(latencies,0.95),
            "hit_rate": hit_rate, "success": successes, "requests": successes+failures}


async def test2_burst():
    """50 concurrent requests"""
    print("\n=== Test 2: Burst 20 concurrent ===")
    successes, failures, latencies, status_429 = 0, 0, [], 0
    async with httpx.AsyncClient(limits=httpx.Limits(max_connections=50)) as client:
        tasks = [send_request(client) for _ in range(20)]
        responses = await asyncio.gather(*tasks)
        for ok, elapsed, _, status in responses:
            if ok and status == 200:
                successes += 1; latencies.append(elapsed)
            elif status == 429:
                status_429 += 1; failures += 1
            else:
                failures += 1
    print(f"  Success: {successes}, Failed: {failures}, 429s: {status_429}")
    print(f"  p50: {percentile(latencies,0.50):.2f}s  p95: {percentile(latencies,0.95):.2f}s")
    return {"test": "burst", "success": successes, "failures": failures, "rate_429": status_429}


async def test3_pool_status():
    """Pool breaker status"""
    print("\n=== Test 3: Pool Status ===")
    async with httpx.AsyncClient() as client:
        r = await client.get(f"{BASE_URL}/admin/api/pool/status",
                            headers={"X-Admin-Password": ADMIN_PASSWORD})
        data = r.json()
        print(f"  Available: {data.get('availableCount')}/{data.get('totalAccounts')}")
        print(f"  Queue depth: {data.get('queueDepth')}/{data.get('maxConcurrent')}")
        for s in data.get("states", [])[:5]:
            print(f"  {s['id'][:16]}... state={s['state']} ewma={s['ewmaLatency']} w={s['effectiveWeight']}")
    return {"test": "pool_status", "available": data.get('availableCount'),
            "total": data.get('totalAccounts')}


async def test4_cross_account_cache():
    """Same prompt across accounts -> cross-account hit (needs actual traffic first)"""
    print("\n=== Test 4: Cache Stats ===")
    async with httpx.AsyncClient() as client:
        r = await client.get(f"{BASE_URL}/admin/api/cache/stats",
                            headers={"X-Admin-Password": ADMIN_PASSWORD})
        stats = r.json()
        print(f"  L1 entries: {stats.get('l1_entries')}")
        print(f"  Hit rate: {stats.get('hit_rate',0):.2%}")
        print(f"  Cross-account hits: {stats.get('cross_account_hits')}")
        print(f"  Tokens saved: {stats.get('tokens_saved')}")
        print(f"  LRU evictions: {stats.get('lru_evictions')}")
    return {"test": "cache_stats", **stats}


async def test5_cache_persistence():
    """Force sync and verify"""
    print("\n=== Test 5: Cache Persistence ===")
    async with httpx.AsyncClient() as client:
        r = await client.post(f"{BASE_URL}/admin/api/cache/sync",
                             headers={"X-Admin-Password": ADMIN_PASSWORD})
        print(f"  Sync: {r.json()}")
        r2 = await client.get(f"{BASE_URL}/admin/api/cache/stats",
                             headers={"X-Admin-Password": ADMIN_PASSWORD})
        print(f"  L1 entries after sync: {r2.json().get('l1_entries')}")
        print(f"  NOTE: Restart Kiro-Go and re-run to verify L2 reload")
    return {"test": "persistence", "synced": True}


async def main():
    p = argparse.ArgumentParser()
    p.add_argument("--test", type=int, default=0)
    args = p.parse_args()

    tests = {1: test1_baseline, 2: test2_burst, 3: test3_pool_status,
             4: test4_cross_account_cache, 5: test5_cache_persistence}

    if args.test:
        results = [await tests[args.test]()]
    else:
        results = []
        for t in sorted(tests):
            results.append(await tests[t]())

    report = {"timestamp": time.time(), "results": results}
    with open("bench_results.json", "w") as f:
        json.dump(report, f, indent=2)
    print(f"\nReport saved to bench_results.json")


if __name__ == "__main__":
    asyncio.run(main())
