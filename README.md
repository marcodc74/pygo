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
./pygo check --json app.pg           # diagnostica per agenti
./pygo test examples/                # esegue i blocchi test "..." {}
```

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

## Architettura

```
cmd/pygo/           CLI
internal/lexer      token, terminatori automatici, interpolazione ${...}
internal/parser     parser a discesa ricorsiva con recupero dagli errori
internal/ast        AST e visitor
internal/sig        firme della stdlib (file .pg incorporati nel binario)
internal/check      checker statico: nomi, tipi, fallibilità, effetti, nil, esaustività
internal/interp     interprete, valori thread-safe, stdlib, goroutine per spawn/chan
internal/printer    stampa canonica dell'AST (base di pygo fmt)
internal/loader     moduli locali (import "./x"), cicli, bundle
```

## Stato

Il progetto è in sviluppo attivo (v0.1).

**Fatto e testato**:
- linguaggio completo, con lexer, parser, checker statico e interprete;
- stdlib descritta sopra;
- concorrenza;
- contratti e test inline;
- capability con `--allow`;
- budget di passi e timeout;
- CLI `run` / `check` / `test` con `--json`.

**In corso**:
- comandi per agenti: `fmt`, `fix` (applica le correzioni proposte), `explain`, `guide` (specifica compatta da mettere nel contesto), `describe`, `outline`, `edit` per simbolo, `ast`;
- `pygo build`, che crea un eseguibile autonomo;
- Dockerfile, manifest Kubernetes, cross-compilazione e CI;
- specifica completa in `docs/`.

**Roadmap**: backend compilato (Go/WASM), trait/interfacce, `select`, LSP,
package manager, record/replay degli effetti.

## Sviluppo

```sh
go vet ./... && go test ./...
```
