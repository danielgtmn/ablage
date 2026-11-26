# Security policy

ablage keeps data apart between tenants (scopes) and builds every SQL statement itself, so security reports are welcome and taken seriously.

## Reporting a vulnerability

**Please do not open a public issue.** Report it privately instead:

1. Go to [Security → Report a vulnerability](https://github.com/danielgtmn/ablage/security/advisories/new).
2. Describe what is affected, how to reproduce it (ideally a Go test), and what an attacker gains. Add the version from your `go.mod`.

You get an answer within a week. Once a fix is released, the advisory is published, and you are credited unless you prefer not to be.

## Supported versions

ablage is before 1.0. Fixes go into `main` and into the next release, which is cut promptly for security fixes. Only the **latest release** receives fixes.

## Scope

In scope: every package in this repository, in particular anything that lets a repository read or write outside its scope, or lets values reach SQL other than as parameters.

Out of scope: vulnerabilities in Go, database drivers or databases themselves (report them upstream; tell us if ablage uses them unsafely), and apps built with ablage, unless ablage's documented use leads to the problem.
