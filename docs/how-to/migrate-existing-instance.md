# Migrowanie istniejącej instancji Home Assistant

> Migracja jest eksperymentalna i nie jest gotowa do użycia produkcyjnego. Pełny, izolowany przebieg backupu i restore został zweryfikowany wyłącznie na HA Container 2026.11.0 oraz jednorazowym workerze w k3d. Nie ma SLO czasu przerwy ani automatycznego przełączenia ruchu. Operator nigdy nie zatrzymuje starej instancji ani nie zmienia Service, Ingress lub Gateway w jej imieniu.

## Ręczna checklista cutoveru

1. Zapisz namespace, nazwę i UID **nowego** `HomeAssistant` oraz lokalizację starego workloadu. Sprawdź, że nowy PVC jest pusty i nie jest współdzielony ze źródłem.
2. Wykonaj wyłącznie odczytowy skan i sprawdź raport. Nie kopiuj raportu do Git: raporty, archiwum i klucze mogą wskazywać dane operacyjne. Obiekty `Unsupported` i `Incomplete` nie są importowane.
3. Przed backupem potwierdź zakres (zwłaszcza bazę), agenta, miejsce na archiwum i bezpieczne miejsce dla klucza. Klucz i token przekazuj przez Secret/referencję, nigdy przez URL, manifest, status ani log.
4. Zatwierdź osobno backup i stabilność jego ustawień. Worker zapisuje chronione archiwum na niezależnym PVC należącym do `HomeAssistantMigration`; deskryptor wiąże UID PVC, rozmiar, digest, wersję klucza i źródłowy wynik. Zapisz identyfikator backupu, agenta i skrót archiwum; utrata odpowiedzi oznacza `Unknown`, nie zgodę na ślepe ponowienie.
5. **Ręcznie zatrzymaj źródło**, a potem potwierdź brak procesu i połączeń. Sama niedostępność sieci nie jest dowodem zatrzymania. Nie uruchamiaj obu instancji z tymi samymi integracjami produkcyjnymi.
6. Tryb `spec.alpha.migrationRestore.enabled: true` jest domyślnie wyłączony i blokuje bootstrap, synchronizację konfiguracji, init-containery oraz projekcje plików do `/config`. Cel może wystartować wyłącznie po związanych zgodach `source-stop` i `restore` oraz przygotowaniu egzekwowanej izolacji onboardingu.
7. Zatwierdź restore powiązany z UID celu, digestem i zakresem archiwum. Worker przesyła archiwum i sprawdza onboarding info przed pojedynczym restore. Błędny klucz, uszkodzone archiwum, brak miejsca, utrata odpowiedzi lub rozbieżność wyniku zatrzymują procedurę. Nie publikuj odpowiedzi endpointu.
8. Po restarcie zweryfikuj tożsamość administratora/operatora, token, MFA, integracje i zatwierdzone dane. Gotowość Poda ani HTTP 2xx nie są dowodem poprawnego restore. Dopiero związana zgoda `resume` zwalnia następną bramkę.
9. Ręcznie przełącz Service/Ingress/Gateway dopiero po osobnej zgodzie `manual-cutover`. Zachowaj źródło wyłączone podczas obserwacji. Powrót może utracić zmiany wykonane po restore i wymaga osobnej ręcznej procedury.
10. Po akceptacji celu wyłącz `migrationRestore.enabled` świadomą zmianą specyfikacji; tylko wtedy zwykły bootstrap i projekcje mogą wrócić do pracy.

Jeśli źródło to Kubernetes `Deployment` lub `StatefulSet` w tym samym namespace,
podaj w `HomeAssistantMigration.spec.sourceWorkload` jego `kind`, `name` i `uid`,
a w zgodzie `source-stop` ten sam `sourceUID` oraz niepusty `digest`. Operator
odczyta aktualne `replicas=0`, zaobserwowaną generację i Pody dopasowane do
selektora; nie zmienia źródła. Zgoda restore musi osobno wskazywać `targetUID`,
`archiveDigest` i `scopeDigest` zgodne z operacją. Dla źródła zewnętrznego nie
ma technicznego potwierdzenia w Kubernetes: administrator pozostaje odpowiedzialny
za jego sprawdzenie i ręczną zgodę. Odczyt jest migawką, nie blokadą ponownego
uruchomienia źródła — nie uruchamiaj go ponownie podczas migracji.

## Zgody, nieznany wynik i anulowanie

Zgoda obowiązuje do atomowego przyjęcia konkretnego żądania. Jej wycofanie przed
przyjęciem blokuje etap; po przyjęciu nie cofa już zewnętrznego żądania HA i nie
pozwala na drugi POST. `Unknown` oznacza, że wynik mutacji nie został bezpiecznie
ustalony. Użyj tylko związanego rozstrzygnięcia `ObserveExternalResult` (odczyt
tego samego dowodu) albo `Abort`; nie usuwaj receiptów ani nie twórz nowej zgody
w celu ponowienia operacji.

Ustawienie `spec.cancelRequested: true`, rozstrzygnięcie `Abort` lub usunięcie
operacji blokuje nowe etapy. Kontroler kończy lokalne Joby i czeka na zniknięcie
ich Podów przed zwolnieniem finalizera. Nie deklaruje cofnięcia zadania już
przyjętego przez HA. PVC archiwum, deskryptor, receipty oraz zasoby workera są
własnością migracji i są usuwane przez garbage collection dopiero po bezpiecznym
zakończeniu lokalnej pracy; nie usuwaj ich ręcznie podczas `Unknown` lub anulowania.

## Skan i zatwierdzony import

Token źródła umieść w Secret w docelowym namespace. URL źródła musi używać
HTTP(S) i nie może zawierać użytkownika, tokenu, parametrów query ani fragmentu.

```sh
ha-operator-migrate scan \
  --source-url https://ha-source.example \
  --source-token-secret ha-source-token \
  --namespace home \
  --target ha-new > migration-report.json
```

Skan nie zapisuje do Home Assistant ani Kubernetes. Sprawdź `id` raportu i
klasyfikacje: `Importable` wymaga zweryfikowanego mappera, `Incomplete` oznacza
brak danych, `Unsupported` oznacza brak bezpiecznego eksportu, a `Conflict`
oznacza kolizję wymagającą ręcznej decyzji. Nie dodawaj raportu do Git.
Sekcja `backup` przedstawia odczytany zakres bazy, dostępność wskazanych
agentów, informację o ochronie kluczem oraz ograniczenia zewnętrznych danych.
`Ready` w tej sekcji oznacza wyłącznie pozytywny preflight, nie wykonany backup
ani potwierdzoną możliwość restore. Klucz nie jest umieszczany w raporcie.

Po ręcznym sprawdzeniu identyfikatora treści utwórz operację tylko z jawnym
potwierdzeniem:

```sh
ha-operator-migrate import \
  --report migration-report.json \
  --report-id sha256:... \
  --namespace home \
  --target ha-new \
  --name ha-existing-migration \
  --source-token-secret ha-source-token \
  --confirm
```

Stan operacji odczytaj bez serializowania Secretów:

```sh
kubectl -n home get homeassistantmigration ha-existing-migration -o yaml
```

CLI wymaga odczytu wskazanego Secretu tokenu źródła i celu oraz
tworzenia/odczytu `HomeAssistantMigration` i `HomeAssistantFloor` w wybranym namespace.
Gdy importowane są dostępne dane uwierzytelniające, wymagane jest także
tworzenie/odczyt wynikowego Secretu. Nie ma
domyślnej komendy mutującej ani domyślnego potwierdzenia.
Opcjonalna flaga `--source-token-secret` zapisuje nazwę i klucz Secretu w operacji,
nie wartość tokenu; klucz można zmienić przez `--source-token-key`. Wykonawca
migracji odczytuje wyłącznie przypiętą referencję w zaakceptowanym żądaniu.
Opcjonalne `--recovery-key-secret` i `--recovery-key-key` wskazują osobno Secret
z kluczem odzyskiwania w tym samym namespace. Przechowuj go oddzielnie od
archiwum. Referencja nie potwierdza poprawności klucza ani nie uruchamia restore.
Po zatwierdzeniu backupu powód `SourceTokenReferenceMissing` oznacza brak
poprawnej referencji, `SourceTokenUnavailable` — brak Secretu lub wartości, a
`BackupExecutorUnavailable` — brak zaimplementowanego wykonawcy. Żaden z tych
stanów nie potwierdza utworzenia kopii.
Zgoda `backup` musi zawierać `approved: true`, `digest` równy `spec.reportID`,
`scopeDigest` równy `spec.backupScopeDigest` oraz `targetUID` równy
`spec.target.uid`. Niezgodne lub brakujące powiązanie
daje `BackupApprovalNotBound`. Raport obejmuje zaobserwowany zakres backupu,
ale jego zatwierdzenie nie zastępuje ponownej kontroli zakresu przed wykonaniem.

## Pauza i późniejsza adopcja

Importowany CR zaczyna w trwałej pauzie. Usunięcie takiego CR nie usuwa obiektu
w Home Assistant. Zdjęcie pauzy **nie** jest zgodą na zapis: rozpoczyna tylko
sprawdzenie tożsamości i kanonicznych pól na aktualnym celu.

- Warunek `MigrationAdoption=True` z powodem `Adopted` potwierdza jedynie
  zgodny odczyt **bez mutacji**. Nie włącza zwykłej synchronizacji ani usuwania
  obiektu HA. Każdy kolejny reconcile ponawia porównanie; zmiana daje konflikt,
  a brak obiektu blokuje adopcję bez ponownego tworzenia.
- Dla konfliktu popraw deklarację, aby odpowiadała HA, albo zachowaj ręczne
  zarządzanie. Zgoda na nadpisanie jest adnotacją CR-a
  `ha.homeassistant.io/migration-overwrite-approval`: JSON ma pola `version`
  (`v1`), `approved` (`true`), `objectUID`, `generation`, `targetUID`,
  `sourceID` i `comparisonDigest` pokazany w warunku. Musi odpowiadać bieżącej
  tożsamości i porównaniu; zmiana wiązania lub usunięcie adnotacji ją unieważnia.
  Zgodę cofniesz poleceniem `kubectl annotate <kind> <name> -n <namespace> ha.homeassistant.io/migration-overwrite-approval-`.
- Nawet ważna zgoda daje `UnsafeOverwriteUnsupported`, ponieważ brak dowodu
  atomowego bezpiecznego zapisu. Usunięcie importowanego CR-a, również przez
  garbage collection po usunięciu migracji, pozostawia obiekt HA i usuwa własny
  finalizer operatora. Nie jest to pełne zarządzanie cyklem życia obiektu HA.

Przykład ustawienia zgody (zastąp wszystkie wartości danymi aktualnego CR-a,
celu i warunku porównania):

```sh
kubectl annotate <kind> <name> -n <namespace> --overwrite \
  'ha.homeassistant.io/migration-overwrite-approval={"version":"v1","approved":true,"objectUID":"<uid-cr>","generation":1,"targetUID":"<uid-ha>","sourceID":"<id-ha>","comparisonDigest":"sha256:<digest>"}'
```

Ten przykład nie włącza nadpisywania; służy do powiązania decyzji z konkretnym
porównaniem. Usuń adnotację, aby wycofać zgodę.

Skan i import Floor przeszły test E2E na HA Container 2026.11.0. Adopcję bez
zapisu, restart, konflikt, zniknięcie obiektu i bezpieczne usunięcie ręcznie
przygotowanych Floor, Area, Label, Scene, Script i Dashboard przetestowano także
na tej wersji; import pozostałych Kindów nie jest tym potwierdzony. Automation oraz Integration pozostają
zablokowane, gdy nie można odczytać całej zarządzanej konfiguracji. Testy te nie
potwierdzają kompletnej migracji ani zgodności wszystkich wersji HA.

## Zakres i bezpieczeństwo

- Importowane CR-y są wstrzymane, a ich usunięcie nie usuwa obiektu w Home Assistant.
- Nie ma automatycznej synchronizacji Secretów ani eksportu danych nieudostępnianych przez API HA.
- `spec.alpha.migrationRestore.enabled` jest w cyklu promocji: alpha opt-in (obecnie), stabilne domyślnie `false`, stabilne domyślnie `true`, następnie obowiązkowe. Każda promocja wymaga E2E i udokumentowanych ograniczeń.
