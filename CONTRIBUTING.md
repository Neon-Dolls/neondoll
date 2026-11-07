# Contributing

## Go source organization

Keep files cohesive and easy to review. Target 250 lines or fewer per Go
source file; 300 lines is the soft limit. Files above 300 lines should be
justified during review, and files above 500 lines should normally be split
along responsibility boundaries. Exceptions are appropriate when splitting
would materially harm clarity. Never split files mechanically merely to meet
a numeric limit.

Existing oversized files may be handled in focused, behavior-preserving
follow-ups rather than mixed into feature changes. In particular, the SQLite
Memory Provider remains a separate refactoring task.
