# Pygo — un linguaggio di programmazione progettato per le IA

Pygo è un linguaggio general-purpose, potente come Python o Go, pensato perché
lo **scriva, legga, corregga ed esegua un modello linguistico (LLM / agente)**.
La leggibilità per un essere umano non è un obiettivo: lo sono invece
l'affidabilità della generazione, la diagnostica leggibile da una macchina e
l'esecuzione sicura di codice generato automaticamente.

Il toolchain è un unico binario Go statico, senza dipendenze, che gira su Linux,
macOS, Windows, in Docker e in Kubernetes.

```
fn main() {
    let nums = [3, 1, 2]
    print("ordinati e raddoppiati: ${nums.map(fn(n) => n * 2).sorted()}")
}
```

## Perché è "per IA"

Ogni scelta di progetto risponde a un limite concreto degli LLM.

| Limite dell'LLM | Scelta di Pygo |
|---|---|
| Genera da sinistra a destra, senza tornare indietro | Grammatica senza ambiguità: blocchi `{}`, niente indentazione significativa, niente `;`, parentesi obbligatorie quando si mescolano `and`/`or` e `??`, confronti non concatenabili |
| Conosce bene solo la sintassi vista in addestramento | Solo le forme più comuni di Go, Rust, Python e TS (`fn`, `let`, `match`, `for x in`, `${}`); keyword brevi, ognuna 1 token |
| Più modi di scrivere la stessa cosa aumentano la varianza | Una sola forma canonica per ogni costrutto |
| Inventa API e nomi | Nomi, campi e metodi sconosciuti sono errori con suggerimento `did you mean` |
| Scambia l'ordine degli argomenti | Solo il primo argomento è posizionale: `transfer(10, from: a, to: b)` (errore `E0306` altrimenti) |
| Dimentica di gestire gli errori | `-> !T` indica una funzione che può fallire; chiamarla senza `try` o `catch` è un errore di compilazione (`E0401`) |
| Dimentica i `nil` | `nil` esiste solo nei tipi `T?`; usare un `T?` senza controllarlo è un errore (`E0310`), con narrowing su `if x != nil` |
| Codice generato = codice non fidato | Gli effetti sono capability (`uses fs, net`) verificate staticamente e a runtime; di default è tutto negato e si abilita con `--allow` |
| Cicli infiniti durante i tentativi | `--max-steps` (budget deterministico) e `--timeout` |
| Lavora a cicli scrivi → esegui → correggi | Diagnostica JSON con codici stabili, hint e correzioni applicabili; panic in JSON con i valori delle variabili coinvolte; contratti `requires`/`ensures`; `test` inline |
| Errori non riproducibili | Mappe ordinate, `rand` con seme, orologio come effetto esplicito, overflow degli interi = errore |

Altre regole: niente shadowing, niente variabili globali mutabili, niente
conversioni implicite (`Int + Float` è un errore), niente "truthiness" (le
condizioni devono essere `Bool`), `match` esaustivo sugli enum.

## Esempio

```
import "json"

struct Line { sku: Str, qty: Int, price: Float }

enum Outcome {
    Accepted(total: Float)
    Rejected(reason: Str)
}

fn line_total(l: Line) -> !Float
    requires l.price >= 0.0
{
    if l.qty <= 0 {
        fail error("invalid quantity", code: "E_QTY", data: l.qty)
    }
    return float(l.qty) * l.price
}

fn evaluate(text: Str) -> Outcome {
    let l = json.decode_as(text, schema: Line) catch e {
        return Outcome.Rejected(reason: e.message)
    }
    let total = line_total(l) catch e { return Outcome.Rejected(reason: e.code) }
    return Outcome.Accepted(total: total)
}

test "rejects zero quantity" {
    let rejected = match evaluate(r"""{"sku": "X", "qty": 0, "price": 1.0}""") {
        Outcome.Rejected(_) => true
        _ => false
    }
    assert rejected
}
```

Altri esempi sono in [`examples/`](examples/).

## Uso

```sh
go build -o pygo ./cmd/pygo          # richiede Go >= 1.22

./pygo run examples/hello.pg
./pygo run --allow fs,net app.pg -- arg1 arg2
./pygo run -e 'print([1, 2, 3].map(fn(x) => x * x).sum())'
./pygo check --json app.pg           # diagnostica per agenti
./pygo test examples/                # esegue i blocchi test "..." {}
```

Comandi pensati per un agente (tutti con output JSON):

| Comando | A cosa serve |
|---|---|
| `pygo guide` | Specifica compatta del linguaggio e firme della stdlib (~2.600 token), da mettere nel contesto del modello |
| `pygo check --json` | Diagnostica con codice stabile, hint e correzione applicabile (`fix`) |
| `pygo fix [--all]` | Applica le correzioni: sicure di default, anche i "did you mean" con `--all` |
| `pygo explain E0306` | Spiega un codice con esempio sbagliato e corretto |
| `pygo describe file.pg\|json` | API di un file o di un modulo stdlib in JSON |
| `pygo outline file.pg` | Simboli (`fn:main`, `struct:User`, ...) con righe e hash del contenuto |
| `pygo edit file.pg --replace fn:nome` | Sostituisce, inserisce o cancella una dichiarazione intera (niente diff per riga); `--expect-hash` rifiuta la modifica se nel frattempo il simbolo è cambiato |
| `pygo fmt [-w]` | Forma canonica unica |
| `pygo ast file.pg` | AST in JSON |
| `pygo build -o app file.pg` | Eseguibile autonomo (runtime + sorgente); con `--runtime` si può usare un binario compilato per un altro OS |

Exit code stabili: `0` ok, `1` failure non gestita in `main`, `2` panic,
`3` errore di compilazione, `4` capability non concessa.

Esempio di diagnostica (`pygo check --json`):

```json
{
  "code": "E0306",
  "severity": "error",
  "message": "argument 2 of transfer must be named (only the first argument is positional)",
  "file": "bad.pg", "line": 5, "col": 18,
  "hint": "write from: \"alice\""
}
```

## Il linguaggio in breve

- **Tipi**: `Int` (64 bit, con controllo dell'overflow), `Float`, `Str`, `Bool`,
  `List[T]`, `Map[K, V]` (ordinata), `T?`, `fn(A) -> !B`, `Chan[T]`, `Task[T]`,
  `Error`, `Any`, più `struct`, `enum` con payload, `impl` con metodi e funzioni
  generiche `fn first[T](xs: List[T]) -> T?`.
- **Variabili**: `let` (immutabile) e `var` (mutabile).
- **Errori**: `fail error("msg", code: "E_X")`, `try f()` propaga l'errore,
  `f() catch e { ... }` lo gestisce. I bug veri (indice fuori range, assert,
  contratti violati) sono panic non catturabili.
- **Concorrenza**: `spawn f(x)` restituisce un `Task`, poi `try t.wait()` o
  `wait_all(tasks)`; `chan(n)` con `send`, `recv`, `close` e `for v in ch`.
- **Stringhe**: `"ciao ${nome}"`, `"${x:.2}"`, `"${n:>5}"`; `r"..."` e
  `"""..."""` per il testo raw o su più righe. `{` e `}` sono caratteri normali,
  quindi il JSON dentro una stringa non va escapato.
- **Libreria standard**: `json`, `fs`, `os`, `http` (client e server con
  shutdown graceful), `time`, `log` (JSON su stderr), `math`, `re`, `proc`,
  `rand`. Le firme sono in [`internal/sig/std/`](internal/sig/std/), scritte in
  Pygo stesso: sono l'unica fonte di verità per checker, runtime e documentazione.

## Deploy: ogni sistema operativo, Docker, Kubernetes

```sh
make dist        # binari statici in dist/: linux, darwin, windows × amd64, arm64
./pygo build --allow net,env -o server examples/server.pg      # un solo eseguibile autonomo
./pygo build --runtime dist/pygo-windows-amd64.exe -o app.exe app.pg   # build per un altro OS

docker build -f deploy/Dockerfile -t pygo-app .                    # immagine dell'app (16 MB, distroless, non-root)
docker build -f deploy/Dockerfile --build-arg APP=mio.pg --build-arg ALLOW=net -t mia-app .
docker build -f deploy/Dockerfile --target toolchain -t pygo .     # immagine con la CLI

kubectl apply -f deploy/k8s/deployment.yaml     # Deployment + Service, probe su /healthz
```

- **Build dell'immagine:** la build Docker esegue `check` e `test` sul programma. Un programma che non passa i controlli non diventa un'immagine.
- **Shutdown:** `http.serve` chiude le connessioni in modo pulito su SIGTERM, come serve per i rolling update di Kubernetes.
- **Log:** `log.info(...)` scrive una riga JSON su stderr, pronta per i raccoglitori di log.
- **Sandbox per codice generato da IA:** `deploy/k8s/job-sandbox.yaml` esegue il programma
  - senza capability Pygo;
  - con budget di passi e timeout;
  - con filesystem in sola lettura;
  - con una NetworkPolicy che blocca tutto il traffico.

  Un ciclo infinito termina con un report JSON (`R0011`) e l'accesso a file o rete senza permesso termina con exit 4.

## Documentazione

- [`docs/SPEC.md`](docs/SPEC.md): specifica completa del linguaggio.
- [`docs/GUIDE.md`](docs/GUIDE.md): guida compatta da mettere nel contesto di un modello (`pygo guide`).
- `pygo explain` spiega tutti i codici diagnostici (E/W) e runtime (R).

Le due guide sono in inglese: costano meno token e sono la lingua più rappresentata nei dati di addestramento dei modelli.

## Architettura

```
cmd/pygo/           CLI
internal/lexer      token, terminatori automatici, interpolazione ${...}
internal/parser     parser a discesa ricorsiva con recupero dagli errori
internal/ast        AST, visitor, export JSON
internal/sig        firme della stdlib (file .pg incorporati nel binario)
internal/check      checker statico: nomi, tipi, fallibilità, effetti, nil, esaustività, correzioni
internal/interp     interprete, valori thread-safe, stdlib, goroutine per spawn/chan
internal/printer    stampa canonica dell'AST (pygo fmt)
internal/loader     moduli locali (import "./x"), cicli, bundle
internal/guide      guida compatta per il contesto dei modelli
deploy/             Dockerfile, manifest Kubernetes
examples/           hello, errors, concurrency, server (HTTP), wordcount (CLI su file)
```

## Stato

v0.1.

**Fatto e testato:**
- **Linguaggio e checker:** linguaggio completo, checker statico, interprete, stdlib e concorrenza.
- **Strumenti per agenti:** tutti i comandi della CLI elencati sopra.
- **Distribuzione:** cross-compilazione per 6 piattaforme.
- **Docker:** immagini costruite e provate (l'app risponde, lo shutdown è pulito, la sandbox blocca cicli infiniti e permessi mancanti).
- **Kubernetes:** i manifest sono sintatticamente validi, ma non li ho applicati a un cluster reale.
- **Test:** unit test, test golden sugli esempi e race detector passano.
- **CI:** GitHub Actions su Linux, macOS e Windows, con build dei binari e smoke test Docker.

**Roadmap:**
- backend compilato (generazione di Go/WASM);
- trait e interfacce;
- `select` su più canali;
- effetti per le funzioni di ordine superiore;
- LSP;
- package manager;
- record/replay degli effetti;
- conservare i commenti normali in `fmt`.

## Sviluppo

```sh
make test     # vet + gofmt + unit test + test degli esempi
make race     # race detector
make dist     # cross-compilazione
```
