# Contributing to Portide

Thanks for helping out. Before your first pull request is merged, please sign the [Contributor License Agreement](CLA.md); CLA Assistant will ask you in the pull request.

## Licensing

- Everything in this repository is licensed under [Apache-2.0](LICENSE), except:
- the relay server, `core/relayserver/`, `cmd/portide-relay/` and `deploy/relay/`, which is licensed under [AGPL-3.0](core/relayserver/LICENSE).

Client code (the rest of `core/`, `app/`, `cmd/portide/`, `desktop/`, `mobile/`) must not import `core/relayserver`, so that clients stay Apache-2.0. Protocol pieces that both sides need belong in `core/relay`.

Your contribution is licensed under the license of the directory it lands in, and under the terms of the CLA.

## Clean-room rule

Portide draws on the public design ideas of LocalSend but does not copy its code. Do not paste code from LocalSend or from other projects whose license is incompatible with the directory you are changing. If you bring in third-party code, say where it comes from and under which license in the pull request.
