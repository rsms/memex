# rsms's memex

Software for managing my digital information, like tweets.


## Usage

First check out the source and build. You'll need Make and Go installed.

```sh
git clone https://github.com/rsms/memex.git
cd memex
make
```

Then make your memex data directory by starting from a copy of the example directory.
This directory can be anywhere you like and will hold your data and service configs.

```sh
cp -a path-to-source-of/memex/example-memexdir ~/memex
```

You can also start with an empty directory, e.g. `mkdir ~/memex`.

Next, export the path of your memex directory in your environment:

```sh
export MEMEX_DIR=$HOME/memex
# optional: add to you shell's init script:
echo "export MEMEX_DIR=$MEMEX_DIR" \
>> ~/`[[ $SHELL == *"/zsh" ]] && echo .zshrc || echo .bashrc`
```

Finally, run the memex service:

```sh
path-to-source-of/memex/bin/memex
```

The `memex` program is a service manager, much like an operating system service manager like
systemd it manages services (processes). It takes care of restarting processes when they exit,
start, restart or stop processes when their configuration files changes and collect all logs
and outputs in one place. This makes it easy to "run a lot of processes" with a single command
(`memex`), and similarly to stop them all (^C.)

The `memex` program looks in your `MEMEX_DIR` for files called `memexservice.yml` which it treats
as "service configurations". These YAML files describes what command to run as the service process,
what arguments to pass it, its environment, it it's been temporarily disabled or not, and so on.

Changes to these files are automatically detected and you don't need to restart memex when making
changes to these "service config" files.

Service processes are started with a working directory of the configuration file.
I.e. `~/memex/foo/bar/memexservice.yml` with `start:./bar` will start a process `./bar` in
the directory `~/memex/foo/bar`.

See `example-memexdir/foo/memexservice.yml` for a full example and more details.
