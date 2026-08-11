#!/bin/bash
set +e
PASS=""
FAIL=""
for d in module02_cpp_core module03_modern_cpp module04_go_ts_concepts module05_skiplist module06_bloom_hash module07_lsm_engine; do
  echo "======== BUILD $d ========"
  if cmake -B "/root/hellocpp/practice/$d/build" -S "/root/hellocpp/practice/$d" && cmake --build "/root/hellocpp/practice/$d/build" -j; then
    bin=$(ls /root/hellocpp/practice/$d/build/module* 2>/dev/null | head -1)
    if [ -x "$bin" ]; then
      if (cd "/root/hellocpp/practice/$d" && "$bin"); then
        echo "PASS $d"
        PASS="$PASS $d"
      else
        echo "FAIL_RUN $d"
        FAIL="$FAIL $d"
      fi
    else
      echo "FAIL_NOBIN $d"
      FAIL="$FAIL $d"
    fi
  else
    echo "FAIL_BUILD $d"
    FAIL="$FAIL $d"
  fi
done
echo "==== SUMMARY ===="
echo "PASS:$PASS"
echo "FAIL:$FAIL"