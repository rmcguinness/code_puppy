---
name: code-review
description: Architecture review guidelines enforcing DRY, SOLID, security checks, and resource hygiene
tags: [review, solid, dry, security, architecture]
version: "1.0.0"
author: "Code Puppy"
---
# Code Review & Architecture Hygiene Guidelines

When reviewing code:
1. **DRY & YAGNI**: Eliminate copy-paste redundancy. Do not build abstractions before having 3 distinct use cases.
2. **SOLID Principles**:
   - Single Responsibility: Keep structs and functions focused.
   - Open/Closed: Extend via interfaces rather than modifying core logic.
   - Dependency Inversion: Depend on abstractions/interfaces rather than concrete implementations.
3. **Resource Leak Auditing**:
   - Ensure `defer resp.Body.Close()`, `defer file.Close()`, and goroutine cancellation contexts are always respected.
4. **Error Handling**:
   - Wrap errors with informative context using `fmt.Errorf("...: %w", err)`. Never swallow errors silently.
