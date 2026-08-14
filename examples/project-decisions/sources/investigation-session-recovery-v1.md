# Investigation: session crash recovery

Crash testing confirmed that accepted operations must survive restart. Durable
replay, operation lease recovery, and projection rebuilds form the recovery
baseline.
