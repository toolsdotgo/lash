# lash

> less annoying sso helper

> for aws sso

## summary

`awscli2` has sso support, but it's pretty annoying. `lash` is a modest go program which smooths the experience.

## quickstart

get the latest binary from the [releases page](https://github.com/toolsdotgo/lash/releases)

```bash
# you must be logged in to aws for the cli
# replace 'darwin' with 'linux' or 'windows' as required
$ unzip lash-darwin-edge.zip

# for windows, this will be quite different
# the directory you put the binary in MUST be in your path
# the binary is called lash on linux and darwin, lash.exe on windows
$ cp ./lash ~/bin

# for convenience, use -init to create an initial configuration file
$ lash -init
```

This will create an initial configuration file in `~/.aws/lash` called `config.json` - feel free to edit this file add account nicknames.

### build from source

if you have a functional `go` toolchain, clone this repo and:

```bash
go install
```

## usage

> use it as account picker or a command shim

`lash` can be run with without arguments. after getting an oidc token (it has to pop the browser to do that), it uses the token to smash the sso `accountlist` and `getrolecredentials` endpoints. the data is cached and the list is presented to the user as "profiles" - a slugified account name and permission set name.

```bash
$ lash
use one of the following roles:
      user-dev-admin
      user-prod-admin
      vault-dev-ro
      vault-prod-ro
```

the first argument is a string which may match a profile name fully or partially. a partial match will print the profile list and indicate which profile names partially matched.

```bash
$ lash user
use one of the following roles:
   ~> user-dev-admin
   ~> user-prod-admin
      vault-dev-ro
      vault-prod-ro
'user' matches more than one profile
```

when the first argument matches a single profile (or a [nickname](#config)), that profile is used to generate the role credentials from sso.

```bash
$ lash user-dev
selected: user-dev-admin

# within the context of this example, the following would also work
$ lash r-d
#      ^^^ the string 'r-d' uniquely matches the profile 'user-dev-admin'
```

those credentials are written to the `~/.aws/credentials` file (by default) with the profile name `default`.

### command shim mode

> a credentials file will not be written in command shim mode

if a second argument is provided, that argument is presumed to be a command and will be `exec'd` with the credentials as environment variables (`AWS_ACCESS_KEY_ID` etc). further arguments are converted to arguments for the command, if provided.

```bash
# spawns zsh process with keys added to the env
$ lash user-dev zsh

# shimming awscli
$ lash user-dev aws ssm get-parameters-by-path --path / --recursive --query Parameters[].Name
```

because `command shim mode` does not write the credentials file, this allows execution of specific commands or scripts "in other accounts" without needing to "switch accounts" (managed credentials file).

```bash
# "log in" to the user-dev account
$ lash user-dev

# list the lambdas in the user-dev account
$ aws lambda list-functions --query Functions[].FunctionName

# compare the list with user-prod
$ lash user-prod aws lambda list-functions --query Functions[].FunctionName

# we're still logged in to the dev account
$ aws sts get-caller-identity
```

### console url

> go on, open another browser window

if the `-u` flag is set before the role name, an AWS Console Signin URL will be generated.

```bash
# just print the url out on stdout and click it/copypasta it manually
$ lash -u user-dev
selected: user-dev
https://signin.aws.amazon.com/federation?Action=login&Destination=https%3A%2F%2Fconsole.aws.amazon.com%2F&SigninToken=...

# pass it directly to your browser, maybe with a specific profile selected
$ google-chrome --profile-directory=Production $(lash -u user-prod)
selected: user-prod
# and suddenly focus is stolen by a browser
```

## config

> use `lash -init` to create the subdirectory and config.json

`lash` expects a configuration file in the location `~/.aws/lash/config.json`. the `lash/` sub-directory will also be used for caching an oidc token and a list of accounts and roles (profiles).

if you're not keen on using `~/.aws`, use the `-d` flag to set a different base directory. maybe use an alias so you don't forget.

the config file must contain `region` and `start_url` keys and may optionally contain a `nicks` key and stripping strings:

```bash
$ <~/.aws/lash/config.json
{
    "region": "ap-southeast-2",
    "start_url": "https://startup.awsapps.com/start",
    "nicks": {
        "lab": "user-lab-admin",
        "log": "user-logs-admin"
    },
    "strip_prefix": "startup-",
    "strip_suffix": "-poweruser"
}
```

### nicknames (nicks)

> you can use `-n` (no nicks) to disable nickname matching

`nicks` (nicknames) can be used to make things even more terse.

```bash
$ lash lab
selected (via nicks): user-lab-admin
```

### stripping repeated strings

`strip_prefix` and `strip_suffix` can be used to remove repeated low-value strings from account names: perhaps some accounts are prefixed with a company name, for example.

## troubleshooting

### refreshing

> clear things out and get the lastest profiles

use the `-r` flag to get yourself out of trouble - it deletes the caches, which:

* generates a new oidc token (browser pop)
* recreates the profiles cache (accounts and roles)

## raw help

```text
──╖  ╭─┐╭──┐ ╥──┤ less annoying sso helper
  ║  ┼─┤┴─┐┼─╫  └───╴╶─╶───────╶─────────╱╴╴╴
──╨──┘ └──┘│ ╨╴
           ╰──╮
              ╰───╶─╴╶

usage: lash [flags] [profile [command [args...]]]

SUMMARY
  lash integrates with aws sso and fully manages the aws credentials file
  use it either as an account picker, a command shim, or to get a console url

FLAGS
  -d  the directory with the creds and lash/ subdirectory (basedir)
  -h  print this help
  -n  dont use the nickname map from config
  -r  refresh the oidc token and the profiles (full refresh)
  -u  generate an aws console url for the chosen role
  -v  print the program version

  -init  initializes the lash config.json file (and lash/ subdirectory) by
         prompting for region and start url values. nullifies any other
         configuration settings (nicks, prefixes, etc).

PROFILES
  lash refers to the combination of an account and permission set as a profile.
  when lash retrieves the list of accounts and roles from aws sso, it combines
  them as "Account Name-Permission Set Name", then slugifies them as
  "account-name-permission-set-name". thus the role "admin" in the "Data Dev"
  account is rendered as "data-dev-admin".

ARGUMENTS
  profile  [optional] a string which uniquely matches a profile
  command  [optional] a command to run with creds in the environ

BASE DIRECTORY
  is probably ~/.aws and must contain the credentials file. lash will write to
  the credentials file without regard for your feelings. see CUSTOM CREDENTIALS
  below if this frightens you.

  use the -init flag to create the subdirectory and an initial config.json if
  you like.

CONFIG FILE
  is JSON - soz
  it's an object with the following top-level keys

  region             the aws region, e.g.,, ap-southeast-2
  start_url          the awsapps sso landing url
  nicks              [optional] an object with keys for role nicknames and the
                     value of the actual role
  role_strip_prefix  [optional] a string to strip from the beginning of a role
                     name. e.g. "team-name-"
  role_strip_suffix  [optional] a string to strip from the end of a role name
                     e.g. "-developer"
  strip_prefix       [optional] a string to strip from the beginning of profile
                     names. e.g., "company-slug-"
  strip_suffix       [optional] a string to strip from the end of profile names

  e.g.: {
    "region": "ap-southeast-2",
    "start_url": "https://startup.awsapps.com/start",
    "nicks": {"lab": "project-lab-poweruser"},
    "strip_prefix": "startup-"
  }

CUSTOM CREDENTIALS
  if you have named, pet creds you need to keep around, use either
  credentials-head or credentials-tail files in the same directory as
  credentials (the aws creds file). they will not be managed and will be
  added to the resulting creds file in a predictable manner.

EXIT CODES
  1   initialization error - probably something is wrong with the os env
  2   cant load config file (lash/config.json)
  3   problem creating lash subdirectory or config file
  4   problem getting or writing the cache files (oidc token and profiles)
  5   problem getting the role credentials (keys - probably an auth thing)
  6   problem managing the credentials file (permissions or existence)
  9   problem with supplied command (command shim mode)
  11  supplied profile slug has no matches or more than one match
  12  problem getting console signin url
  64  incorrect invocation (usage)
```
