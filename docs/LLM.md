# Usare Pygo con i modelli linguistici

Pygo è progettato per essere scritto da un'IA, ma nessun modello lo ha
visto durante l'addestramento. Qualunque modello lo usa bene se ha due cose:

1. **Il riferimento del linguaggio nel contesto.** È l'output di
   `pygo guide`: sintassi, tipi, errori, effetti e tutte le firme della stdlib,
   in circa 3.000 token.
2. **Il ciclo con il toolchain.** Il modello scrive il programma, poi
   `pygo check --json`, `pygo fix`, `pygo test --json` e `pygo run --json`, e
   corregge leggendo i codici e gli hint.

Ci sono tre modi di farlo, dal più semplice al più integrato:

| Modo | Per chi | Cosa serve |
|---|---|---|
| [Chat](#1-chat-chatgpt-claudeai-gemini) | provare Pygo senza installare niente sul lato IA | copiare `pygo guide` nella chat |
| [CLI di coding](#2-cli-e-agenti-di-coding) | lavorare su un progetto con un agente | `AGENTS.md` (o l'equivalente) nel progetto |
| [API](#3-api-claude-openai-gemini-modelli-locali) | costruire un proprio agente o servizio | `integrations/agent.py` e `integrations/pygo_tools.py` |

Tutti i file citati sono nella cartella [`integrations/`](../integrations).

---

## 1. Chat (ChatGPT, Claude.ai, Gemini)

1. Esegui `pygo guide > pygo-guide.md`.
2. Inserisci il file dove l'app tiene le istruzioni permanenti:

   | App | Dove |
   |---|---|
   | ChatGPT | Istruzioni di un Progetto o di un GPT personalizzato, o come allegato |
   | Claude.ai | Conoscenza e istruzioni di un Progetto |
   | Gemini | Istruzioni di una Gem, o come allegato |

3. Aggiungi questa istruzione: *"Scrivi programmi Pygo seguendo
   esattamente questo riferimento; usa solo le funzioni elencate."*
4. Esegui il codice in locale con `pygo check --json file.pg` e
   `pygo run --json file.pg`, poi incolla l'output JSON nella chat: il
   modello corregge usando i codici e gli hint.

È il modo più lento, perché il ciclo lo fai tu a mano, ma funziona con qualunque modello.

---

## 2. CLI e agenti di coding

Tutte le CLI di coding leggono un file di istruzioni del progetto e possono
eseguire comandi. Basta dare loro:
- [`integrations/AGENTS.md`](../integrations/AGENTS.md), che dice al modello
  di leggere `pygo guide` e di seguire il ciclo check → test → run;
- il permesso di eseguire `pygo` senza chiedere conferma ogni volta.

**Preparazione comune:**
1. Installa `pygo` nel `PATH` (vedi il [README](../README.md#installazione)).
2. Copia `integrations/AGENTS.md` nella radice del progetto:
   `cp integrations/AGENTS.md ./AGENTS.md`.
3. Segui la sezione del tuo strumento.

Riepilogo:

| Strumento | File di istruzioni | Esecuzione non interattiva |
|---|---|---|
| Claude Code | `CLAUDE.md` (con `@AGENTS.md`) + skill | `claude -p "..."` |
| OpenAI Codex CLI | `AGENTS.md` | `codex exec "..."` |
| Gemini CLI | `GEMINI.md`, o `AGENTS.md` da impostazioni | `gemini -p "..."` |
| opencode | `AGENTS.md` | `opencode run "..."` |
| GitHub Copilot CLI | `AGENTS.md` / `.github/copilot-instructions.md` | `copilot -p "..."` |
| Cursor (editor e `cursor-agent`) | `AGENTS.md` / `.cursor/rules/` | `cursor-agent -p "..."` |
| Qwen Code | `QWEN.md` | `qwen -p "..."` |
| Aider | `--read AGENTS.md` | `aider --message "..."` |

I nomi dei file e delle opzioni sono quelli documentati da ciascuno
strumento: queste CLI cambiano spesso, quindi in caso di dubbio controlla
`--help` o la loro documentazione.

### Claude Code

Claude Code legge `CLAUDE.md`, che può includere altri file con `@`.

1. Crea `CLAUDE.md` nella radice del progetto con questa riga:
   ```
   @AGENTS.md
   ```
2. **Skill (consigliata).** Copia
   [`integrations/claude-code/skills/pygo`](../integrations/claude-code/skills/pygo)
   in `.claude/skills/pygo/`, oppure in `~/.claude/skills/pygo/` per tutti i
   progetti. Claude la carica da sola quando il lavoro riguarda file `.pg`.
3. **Permessi.** In `.claude/settings.json`:
   ```json
   {
     "permissions": {
       "allow": [
         "Bash(pygo check:*)", "Bash(pygo fix:*)", "Bash(pygo test:*)",
         "Bash(pygo run:*)", "Bash(pygo fmt:*)", "Bash(pygo guide:*)",
         "Bash(pygo explain:*)", "Bash(pygo outline:*)", "Bash(pygo edit:*)"
       ]
     }
   }
   ```
4. Uso:
   ```sh
   claude                                   # interattivo
   claude -p "scrivi un server HTTP in Pygo con /healthz e /items"
   ```

### OpenAI Codex CLI

Codex legge `AGENTS.md` dalla radice del progetto (e da `~/.codex/AGENTS.md`
per le regole globali), quindi basta la preparazione comune.
```sh
codex                                       # interattivo
codex exec "aggiungi i test a examples/wordcount.pg e falli passare"
```
Nella modalità sandbox predefinita Codex può eseguire `pygo` nella cartella
del progetto. Un programma Pygo che usa la rete ha bisogno sia di `--allow net`
sia di una sandbox di Codex che conceda la rete.

### Gemini CLI

Gemini CLI legge `GEMINI.md`. Hai due possibilità:
- rinomina o copia il file: `cp AGENTS.md GEMINI.md`;
- oppure fagli leggere `AGENTS.md` da `.gemini/settings.json`:
  ```json
  { "context": { "fileName": ["AGENTS.md", "GEMINI.md"] } }
  ```

```sh
gemini                                      # interattivo
gemini -p "scrivi un programma Pygo che conta le parole di un file"
```

### opencode

[opencode](https://opencode.ai) legge `AGENTS.md` dalla radice del progetto
(e `~/.config/opencode/AGENTS.md` per le regole globali). Funziona con
qualunque provider configurato: Claude, GPT, Gemini o modelli locali.

1. Nella radice del progetto, crea `opencode.json` per autorizzare `pygo`
   senza conferma:
   ```json
   {
     "$schema": "https://opencode.ai/config.json",
     "instructions": ["AGENTS.md"],
     "permission": {
       "bash": { "pygo *": "allow", "*": "ask" }
     }
   }
   ```
2. **Opzionale, un agente dedicato.** Crea `.opencode/agent/pygo.md`:
   ```markdown
   ---
   description: Scrive e verifica programmi Pygo
   mode: primary
   ---
   Prima di scrivere codice esegui `pygo guide` e seguilo alla lettera.
   Dopo ogni modifica: `pygo check --json`, poi `pygo test --json`, poi `pygo run --json`.
   ```
3. Uso:
   ```sh
   opencode                                   # interfaccia nel terminale (Tab cambia agente)
   opencode run "scrivi un client HTTP in Pygo con retry"
   ```

### GitHub Copilot (CLI, agente e editor)

- **Copilot CLI** e il **coding agent** leggono `AGENTS.md`.
- **Copilot nell'editor** legge `.github/copilot-instructions.md`:
  `mkdir -p .github && cp AGENTS.md .github/copilot-instructions.md`.

```sh
copilot                                     # interattivo
copilot -p "correggi gli errori di pygo check in app.pg" --allow-tool 'shell(pygo)'
```

### Cursor (editor e `cursor-agent`)

Cursor legge `AGENTS.md` dalla radice del progetto. In alternativa, crea una
regola in `.cursor/rules/pygo.mdc`:
```markdown
---
description: Pygo
globs: ["**/*.pg"]
alwaysApply: false
---
@AGENTS.md
```

Da terminale: `cursor-agent -p "..."`.

### Qwen Code

Qwen Code legge `QWEN.md`: `cp AGENTS.md QWEN.md`. Poi usa `qwen` in modo
interattivo, oppure `qwen -p "..."`.

### Aider

Aider non esegue comandi da solo, ma lancia i test dopo ogni modifica e ne
mostra l'output al modello:
```sh
aider --read AGENTS.md --read docs/GUIDE.md --test-cmd "pygo test --json ." --auto-test app.pg
```
`docs/GUIDE.md` è la stessa guida di `pygo guide`: con Aider conviene
passarla direttamente, perché il modello non può eseguire comandi da solo.

---

## 3. API (Claude, OpenAI, Gemini, modelli locali)

[`integrations/pygo_tools.py`](../integrations/pygo_tools.py) dà a qualunque
modello con function calling un toolchain Pygo isolato, e usa solo la
libreria standard di Python.

**Cosa contiene:**
- `system_prompt()`: le regole del ciclo, più `pygo guide` del binario installato;
- cinque tool:

  | Tool | Cosa fa |
  |---|---|
  | `pygo_check` | diagnostica |
  | `pygo_fix` | restituisce il codice corretto |
  | `pygo_test` | esegue i test |
  | `pygo_run` | esegue `main` |
  | `pygo_explain` | spiega un codice |

- le definizioni dei tool nel formato di ogni provider:

  | Metodo | Formato |
  |---|---|
  | `anthropic_tools()` | Anthropic |
  | `openai_tools()` | OpenAI, Responses API |
  | `openai_chat_tools()` | Chat Completions (anche modelli locali) |
  | `gemini_declarations()` | Gemini |

- `execute(name, args)`: esegue una chiamata in una cartella temporanea
  nuova, con budget di passi (`--max-steps`), timeout e **nessuna capability**,
  salvo quelle concesse con `PygoTools(allow_caps=[...])`.

[`integrations/agent.py`](../integrations/agent.py) è un agente completo di
circa 150 righe per i quattro casi.

**Installazione:**
```sh
cd integrations
pip install anthropic                  # oppure: openai / google-genai
```

**Uso:**
```sh
export ANTHROPIC_API_KEY=...
python agent.py --provider claude "scrivi un programma che stampa i primi 20 numeri primi, con test"

export OPENAI_API_KEY=...
python agent.py --provider openai "..."

export GEMINI_API_KEY=...
python agent.py --provider gemini "..."

# modelli locali: Ollama, vLLM, llama.cpp o LM Studio (endpoint compatibile OpenAI)
ollama pull qwen2.5-coder:32b
OPENAI_BASE_URL=http://localhost:11434/v1 python agent.py --provider local "..."
```

**Opzioni (variabili d'ambiente):**
- `PYGO_MODEL` sceglie il modello. I predefiniti sono `claude-sonnet-5`,
  `gpt-5`, `gemini-2.5-pro` e `qwen2.5-coder:32b`; cambiali con i modelli più
  recenti a cui hai accesso.
- `PYGO_ALLOW=clock,rand` concede capability ai programmi generati.

**Dettagli per provider:**
- **Claude (Messages API):** il riferimento è marcato con `cache_control`,
  quindi dal secondo turno costa molto meno ed è più veloce.
- **OpenAI (Responses API):** il riferimento va in `instructions`, e gli output
  dei tool tornano come `function_call_output`.
- **Gemini (google-genai):** la chiamata automatica delle funzioni è
  disattivata, perché è l'agente che esegue i tool e rimanda i risultati come
  `function_response`.
- **Modelli locali:** serve un modello con supporto ai tool e almeno 16k token
  di contesto (il riferimento ne occupa circa 3k). I modelli sotto i ~14B
  parametri tendono a ignorare le regole: il ciclo con `pygo check` li
  corregge, ma servono più turni.

**Verifica.** `python3 integrations/pygo_tools.py` esegue un autotest
senza modelli. Ho provato il ciclo di `agent.py` con gli SDK reali
(`anthropic`, `openai`, `google-genai`) e risposte HTTP simulate: per ogni
provider, l'agente riceve una chiamata a `pygo_run`, esegue il programma,
rimanda il risultato nel formato giusto e termina. Non l'ho provato contro
le API vere, perché non ho le chiavi.

### Integrarlo nel tuo agente

```python
from pygo_tools import PygoTools

tools = PygoTools(allow_caps=["clock"], max_steps=5_000_000, timeout_s=10)
system = tools.system_prompt()           # mettilo nel system prompt (in cache se possibile)
specs = tools.anthropic_tools()          # o openai_tools() / openai_chat_tools() / gemini_declarations()
# ... quando il modello chiama un tool:
output = tools.execute(call.name, call.arguments)   # stringa JSON da rimandare al modello
```

---

## Sicurezza

Il codice generato da un modello va trattato come non fidato.

- **Nessuna capability di default.** Senza `--allow`, un programma Pygo non
  può toccare file, rete, variabili d'ambiente o processi (exit code 4).
  `pygo_tools` concede solo le capability dichiarate dall'host in `allow_caps`.
- **Budget e timeout.** `--max-steps` e `--timeout` fermano cicli infiniti
  e programmi lenti.
- **`python` equivale a concedere tutto**, perché il codice Python non ha
  sandbox. Non concederlo a codice non fidato.
- **In produzione**, esegui i tool dentro un container. Usa l'immagine
  `toolchain` di `deploy/Dockerfile`, oppure il Job Kubernetes
  `deploy/k8s/job-sandbox.yaml` (nessuna capability, budget, timeout,
  NetworkPolicy che blocca tutto il traffico).
- **Nelle CLI di coding**, autorizza senza conferma solo i comandi `pygo`,
  non tutta la shell.

## Consigli per ottenere buon codice

- **Riferimento aggiornato.** Metti sempre `pygo guide` nel contesto, non
  una tua sintesi: è generato dal binario e corrisponde esattamente alla
  versione installata.
- **Test prima dell'esecuzione.** Chiedi `test "..." { }` accanto alla
  logica: i failure mostrano i valori degli operandi, e il modello corregge
  meglio.
- **Codici stabili.** `E0306` significa sempre la stessa cosa. `pygo explain`
  dà l'esempio sbagliato e quello giusto.
- **Modifiche per simbolo.** Per file grandi, `pygo outline` + `pygo edit
  --replace fn:nome --expect-hash H` evitano diff riga per riga fragili.
- **Librerie Python.** Fai generare le dichiarazioni a
  `pygo extern python modulo nomi...` invece di farle scrivere al modello.
