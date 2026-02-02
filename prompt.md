# Application 'fastsend'
- Program a fast file transfer tool in golang.
- the transfer runs on a trusted local network, no encryption, authentication or alike
- relative filename, size and content matter; no other meta attribute needs transfer
- the fastsend.json file is the configuration file for the server and client

## Server
- Program functions as a server when called with no arguments

## Client
- Program is called with arguments
    - first argument is the server to connect to
    - others are files and directories to transfer

# Testing
- use `test-source` to place arbitrary files and directories in
- use `test-target` as the destination for files, used by the server
- test file transfer, use `cmp` to compare files
- fix problems

# Instructions
- When the design changes in the chat, update this document to match.
- Always format the source code.

//todo start implementing