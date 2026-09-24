---
name: testing-tdd
description: Test-Driven Development (TDD) cycle with unit testing and regression guarantees
tags: [testing, tdd, unit-tests, quality]
version: "1.0.0"
author: "Code Puppy"
---
# Test-Driven Development (TDD) Protocol

Follow the strict Red -> Green -> Refactor cycle when writing or modifying code:

1. **Red Phase (Write Failing Tests First)**
   - Target concretions, not abstractions.
   - Assert expected outputs against actual inputs.
   - Run tests and confirm they FAIL for the expected reason.

2. **Green Phase (Implement Minimal Compliant Code)**
   - Write only the code required to make tests pass.
   - Avoid speculative generalization or premature optimization.

3. **Refactor Phase (Flocking Rules)**
   - Find the most alike code patterns.
   - Identify the smallest difference.
   - Make the simplest change to eliminate the difference.
   - Ensure all tests continue to pass (`go test -race ./...`).
