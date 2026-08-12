#!/bin/bash
set -e
go build -o pebble-bench ./cmd/pebble-bench
./pebble-bench --mode=A --entries=1000000 --engines=sealing_chunk_scan,chunk_scan > bench_1m_A.log
./pebble-bench --mode=B --entries=1000000 --engines=sealing_chunk_scan,chunk_scan > bench_1m_B.log
./pebble-bench --mode=C --entries=1000000 --engines=sealing_chunk_scan,chunk_scan > bench_1m_C.log
./pebble-bench --mode=A --entries=20000000 --engines=sealing_chunk_scan,chunk_scan > bench_20m_A.log
./pebble-bench --mode=B --entries=20000000 --engines=sealing_chunk_scan,chunk_scan > bench_20m_B.log
./pebble-bench --mode=C --entries=20000000 --engines=sealing_chunk_scan,chunk_scan > bench_20m_C.log
./pebble-bench --mode=C --entries=1000000 --engines=sealing_chunk_scan,chunk_scan --readers=5 > bench_1m_readers.log
