# Authenticating to GitHub as an App

Spec §29 says "GitHub App credentials". This system drifted to personal access
tokens, and the difference matters most at exactly one point: the credential a
coding agent holds while it works on your repository.

| | Personal access token | Installation token |
|---|---|---|
| Lifetime | until revoked | one hour |
| Reach | every repository its owner can see | the repositories the App is installed on |
| Narrowing | none | minted per repository, per call |
| Permissions | whatever the owner granted | only what the App was granted |

A leaked PAT is a standing key to everything you own. A leaked installation
token is an hour on one repository. That is the whole argument.

## Creating the App

This is the one step nobody can do for you: creating a GitHub App is a browser
flow.

1. **Settings → Developer settings → GitHub Apps → New GitHub App**
2. Name it whatever you like. Homepage URL can be your repository.
3. **Uncheck "Active" under Webhook** — this system polls and does not receive
   webhooks, and an inactive webhook is one less thing to secure.
4. Repository permissions:

   | Permission | Level | Why |
   |---|---|---|
   | Contents | Read and write | clone, and push the `agent/**` branch |
   | Pull requests | Read and write | §19's pull request |
   | Issues | Read | §25's intake |
   | Checks | Read | the §11.3 red gate and §19 green gate read check runs |
   | Metadata | Read | mandatory, granted automatically |
   | Workflows | Read and write | ONLY if you want day-0 import to open a PR adding CI. Leave this off otherwise: it is the one permission that lets a credential change the checks that verify the work. |

5. **Where can this App be installed?** — "Only on this account".
6. Create it, then **Generate a private key**. You get a `.pem` download; that
   file is the credential and GitHub will not show it again.
7. Note the **App ID** from the App's settings page.
8. **Install App** → choose "Only select repositories" → pick the repository
   you are pointing this at.

## Configuring it

```yaml
github:
  app:
    appId: "123456"
    privateKey: |
      -----BEGIN RSA PRIVATE KEY-----
      ...the contents of the .pem...
      -----END RSA PRIVATE KEY-----
```

Both PKCS#1 (`BEGIN RSA PRIVATE KEY`, what GitHub hands out) and PKCS#8
(`BEGIN PRIVATE KEY`, what some tooling converts it to) are accepted, because
an operator pasting a key should not have to know which one they have.

Prefer `github.existingSecret` for a real install so the key never sits in a
values file.

## What happens then

The control plane logs `authenticating to GitHub as an App` at startup and
every GitHub call mints an installation token scoped to the repository it is
about, cached until ten minutes before expiry.

**The tokens stay as a fallback.** An install without an App keeps working on
`github.token`, and the App failing to load is logged rather than fatal — an
optional, better credential being absent should not stop the engine.

## What this does not yet do

Coding Jobs still receive the long-lived `agent-git` credential by Secret
reference, not a minted one. Handing a Job a short-lived token needs a per-run
Secret and the RBAC to create it, and that is the next change. Until then the
App improves the control plane's own calls and day-0 import; the agent's push
credential is still whatever you configured.

Scope that one narrowly in the meantime: a fine-grained token, one repository,
`contents: write` and nothing else. Never `workflows` — the runner refuses to
commit under `.github/` for the same reason.
