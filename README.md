# puffin

A terminal console for a Kubernetes cluster and the services on it: what is
running, what is deployed, what the flags say, and what each service's API
claims about itself. It holds no roster, because membership is answering
`/api` rather than being on a list.

    go build -o bin/puffin ./cmd/puffin
    bin/puffin

`puffin` on its own opens the console. With a subcommand it answers one
question and exits: `status`, `flags`, `get`, `set`, `logs`, `stop`,
`start`, `restart`, `exec`, `sh`, `repos`. `puffin --help` prints the lot
and then explains addressing.

It reads any cluster and changes only the one named by
`PUFFIN_HOME_CONTEXT`, which defaults to `k3d-local`. kubectl does the
talking, so its kubeconfig and its auth are the ones in force.

- `USING.md` -- the panes, the keys, the subcommands, the themes
- `DESIGN.md` -- what it is for, and why it is shaped this way
- `SEAMS.md` -- what puffin offers something that draws on it

The Go gopher was designed by Renee French and is licensed under CC BY 4.0.
`LICENSE.txt` carries puffin's own terms and `NOTICE` what it borrows.
