# Application 'fastsend'
- Program a fast file transfer tool in golang.
- the transfer runs on a trusted local network, no encryption, authentication or alike
- relative filename, size and content matter; no other meta attribute needs transfer
- the fastsend.json file is the configuration file for the server and client

## Server
- Program functions as a server when called with no arguments
- If a destination file already exists, skip receiving it (discard incoming bytes and report skipped).
- printouts
    - listening on port <portnumber>
    - report incoming connections
    - skipped files
    - completed files

## Client
- Program is called with arguments
    - first argument is the server to connect to
    - others are files and directories to transfer
- printouts
    - connection to server
    - files that were skipped
    - completed files
    - statistics regarding speed in bytes per seconds, use kB, MB, GB if values are large enough
    - periodic printouts (once per second) with statistics regarding transfered bytes per second

# Testing
- use `test-source` to place arbitrary files and directories in
- use `test-target` as the destination for files, used by the server
- test file transfer, use `cmp` to compare files
- fix problems
- remove `test-target` after tests

# Instructions
- When the behavior/design changes in the chat, update this document to match.
- Always format the source code.
- After code changes, always build the program and fix errors.
- Keep responses concise and action-focused.
- Avoid over-explaining or over-checking; only verify what is necessary.
- Prefer short summaries; include details only when they affect next steps.
