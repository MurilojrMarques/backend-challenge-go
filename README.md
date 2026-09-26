# Wallet Service

Serviço de carteira e processamento distribuído de apostas em Go. Recebe operações financeiras de provedores de jogos por HTTP e por SQS, aplica cada uma exatamente uma vez sobre a carteira do jogador, mantém um ledger imutável e publica eventos por outbox transacional. Três instâncias rodam em paralelo sobre o mesmo PostgreSQL sem perder nem duplicar dinheiro.

## Stack

| Peça | Escolha | Motivo |
|---|---|---|
| Linguagem | Go 1.27, binário único `cmd/server` | Papéis selecionados por `APP_ROLES` |
| Injeção e ciclo de vida | Uber Fx | Grafo validado em teste, start e stop ordenados |
| HTTP | chi | Router leve, middlewares componíveis, sem framework |
| Banco | PostgreSQL 17 com pgx/v5 | `SELECT ... FOR UPDATE`, `SKIP LOCKED`, constraints como última barreira |
| Migrações | golang-migrate embutido em `cmd/migrate` | Roda como job antes das réplicas |
| Fila | SQS FIFO via LocalStack | Ordem por grupo, deduplicação por id |
| Autenticação | Keycloak, OIDC com go-oidc | Tokens de client credentials com papéis do realm |
| Observabilidade | slog em JSON, Prometheus em `/metrics` | Logs com correlação, métricas de negócio e HTTP |

## Como rodar

Requisitos: Docker com Compose v2. Go 1.27 apenas para os testes locais.

```
make up          # postgres, keycloak, localstack, migrações e uma instância da API em :8080
make logs        # acompanha a aplicação
make down        # derruba tudo e apaga os volumes
```

Sem `make`, os comandos equivalentes são:

```
docker compose up --build
go test ./...
go test -race ./...
go vet ./...
```

Todas as variáveis têm valor padrão no `docker-compose.yml`. Copie `.env.example` para `.env` só se quiser sobrescrever algo. As filas, os papéis do banco e o realm do Keycloak são criados automaticamente pelos scripts em `deploy/`.

### Migrações

As migrações ficam em `migrations/` e são embutidas no binário `migrate`, que roda como o papel `wallet_migrator`. O compose executa `migrate up` antes de subir a aplicação. Para operar manualmente:

```
docker compose run --rm migrate up          # aplica tudo
docker compose run --rm migrate down 1      # reverte a última
docker compose run --rm migrate down all    # reverte todas
docker compose run --rm migrate version     # versão atual e flag dirty
docker compose run --rm migrate force 3     # corrige a versão após uma falha manual
```

Fora do compose, `go run ./cmd/migrate up` com `DATABASE_URL` apontando para o banco. Cada migração tem o par `up` e `down`; o `down` remove tabelas, triggers e funções na ordem inversa.

Portas: API `8080`, Keycloak `8180`, LocalStack `4566`, Postgres `5432`. Com o compose de teste sobem três réplicas em `8081`, `8082` e `8083`:

```
make e2e-up
```

### Tokens

Peça um token ao Keycloak com `client_credentials`. Os clients do realm `wallet`:

| Client | Papel no token | Uso |
|---|---|---|
| `internal-service` | `wallet-internal` | abre e consulta carteiras, reconcilia |
| `provider-a`, `provider-b` | `wallet-provider` com claim `providerId` | envia e consulta operações do próprio provedor |
| `provider-short-lived` | `wallet-provider`, token de 5s | testes de expiração |
| `no-role` | nenhum | prova que um token válido sem papel recebe 403 |

O secret de cada client é `<client>-secret`.

```
curl -s -X POST http://localhost:8180/realms/wallet/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=provider-a -d client_secret=provider-a-secret
```

Tokens duram 5 minutos. A API aceita `Authorization: Bearer <token>` e exige a audience `wallet-api`.

### Postman

Importe `deploy/postman/wallet.postman_collection.json` e o environment `deploy/postman/local.postman_environment.json`, e selecione o environment. A collection não carrega segredos: URLs, secrets dos clients e credenciais da AWS vêm do environment, e as variáveis sensíveis estão marcadas como `secret`. O environment versionado só contém os valores fixos do compose local; para qualquer outro ambiente crie um environment próprio, que o `.gitignore` já mantém fora do repositório. O script da collection pede e renova os tokens sozinho. Rode `00 Novo cenário` para gerar um jogador e identificadores novos, e siga as pastas na ordem: saúde, carteira, apostas, reversão antes da referência, erros de contrato e SQS. Cada requisição traz asserções na aba Tests, então o Runner da collection executa o roteiro inteiro. Para bater em outra réplica, troque `baseUrl` para `http://localhost:8081`, `8082` ou `8083` com a stack de teste no ar.

## Papéis da aplicação

`APP_ROLES` escolhe o que cada processo executa. Sem valor, todos os papéis ficam ativos.

| Papel | Responsabilidade |
|---|---|
| `api` | rotas de negócio em HTTP e verificação OIDC |
| `consumer` | consome `wager-transactions.fifo` |
| `outbox` | publica eventos pendentes em `wallet-events.fifo` |
| `pending` | resolve operações à espera de referência |

`/health/live`, `/health/ready` e `/metrics` existem em qualquer papel. Uma instância só `api` não abre cliente SQS e não reporta a fila no readiness.

## Contratos HTTP

Todos os corpos são JSON. Valores monetários viajam como string com duas casas decimais e código ISO 4217. Nunca como número. Moedas aceitas: `BRL`, `USD`, `EUR`.

### Carteiras

`POST /wallets` com token interno.

```json
{ "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "initialBalance": { "amount": "1000.00", "currency": "BRL" } }
```

Resposta `201`:

```json
{ "id": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "balance": { "amount": "1000.00", "currency": "BRL" },
  "version": 1, "createdAt": "...", "updatedAt": "..." }
```

Saldo positivo cria a transação `OPENING` já `PROCESSED`, o lançamento de crédito e os eventos `WagerTransactionProcessed` e `WalletBalanceChanged` no mesmo commit. Saldo zero cria só a carteira. Segunda carteira para o mesmo jogador e moeda: `409 WALLET_ALREADY_EXISTS`.

- `GET /wallets/{walletId}`: carteira atual.
- `GET /wallets/{walletId}/ledger?limit=50&cursor=...`: lançamentos em ordem estável de `(createdAt, id)`. `nextCursor` é opaco e só aparece quando há mais páginas. `limit` vai de 1 a 200, padrão 50.
- `POST /wallets/{walletId}/reconciliation`: reconstrói o saldo a partir do ledger na mesma transação em que lê a carteira. `difference` é o saldo armazenado menos o reconstruído. Divergência responde `consistent: false`, gera log de erro e incrementa `wallet_reconciliations_total{consistent="false"}`. Nunca altera saldo.

### Operações de aposta

`POST /wagering/transactions` com token de provedor e header obrigatório `Idempotency-Key`.

```json
{ "providerId": "provider-a", "externalTransactionId": "transaction-123",
  "playerId": "...", "walletId": "...", "roundId": "round-987", "gameId": "fortune-chimp",
  "kind": "BET", "money": { "amount": "25.00", "currency": "BRL" } }
```

`kind` aceita `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK`. `REFUND` e `ROLLBACK` exigem `referenceExternalTransactionId`; `WIN` aceita opcionalmente; `BET` e `LOSS` não aceitam. `LOSS` não movimenta saldo e exige valor zero.

| Status | Significado | Corpo |
|---|---|---|
| `200` | `PROCESSED`, ou replay de qualquer estado final | `transactionId`, `status`, `balance`, `idempotentReplay` |
| `202` | `PENDING_REFERENCE`: a referência ainda não chegou | `transactionId`, `status`, `idempotentReplay` |
| `422` | `REJECTED` por regra de negócio | mais `failureCode` e `correctable` |

`balance` é o saldo observado no processamento original e não muda em replays. Códigos de rejeição:

| `failureCode` | Quando | `correctable` |
|---|---|---|
| `INSUFFICIENT_FUNDS` | débito maior que o saldo | não |
| `REVERSAL_INSUFFICIENT_FUNDS` | rollback de um crédito sem saldo para devolver | não |
| `BALANCE_LIMIT_EXCEEDED` | crédito estouraria o limite de `int64` | não |
| `CURRENCY_MISMATCH` | moeda diferente da carteira | sim |
| `WALLET_PLAYER_MISMATCH` | carteira não pertence ao jogador | sim |
| `REFERENCE_NOT_FOUND` | referência não chegou dentro do prazo | não |
| `REFERENCE_NOT_PROCESSED` | referência foi rejeitada ou falhou | não |
| `REFERENCE_KIND_NOT_ALLOWED` | tipo não pode referenciar aquele tipo | sim |
| `REFERENCE_MISMATCH` | referência de outro provedor, rodada, jogador ou carteira | sim |
| `REFERENCE_AMOUNT_MISMATCH` | reversão com valor diferente da referência | sim |
| `REFERENCE_ALREADY_REVERSED` | a referência já foi revertida com sucesso | não |

`PERMANENT_FAILURE` marca `FAILED`, o estado em que o resolvedor de pendências desiste após erro de integridade.

- `GET /wagering/transactions/{transactionId}`: visão completa, incluindo `referenceAttempts`, `resolvedReferenceId`, `failureCode` e `completedAt`. Provedores só enxergam as próprias.
- `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}`: mesma visão por identificador externo. Outro provedor recebe `404`.

### Erros

Corpo padrão: `{ "code": "...", "message": "...", "correlationId": "..." }`. O `correlationId` vem do header `X-Correlation-Id` quando enviado e volta sempre no header da resposta.

| HTTP | `code` |
|---|---|
| `400` | `MALFORMED_BODY`, `MISSING_IDEMPOTENCY_KEY`, `INVALID_INPUT`, `INVALID_MONEY`, `INVALID_MONEY_SCALE`, `INVALID_AMOUNT_FOR_KIND`, `INVALID_REFERENCE`, `KIND_NOT_ALLOWED`, `INVALID_CURSOR` |
| `401` | `UNAUTHENTICATED`, com `WWW-Authenticate` |
| `403` | `FORBIDDEN` |
| `404` | `NOT_FOUND` |
| `405` | `METHOD_NOT_ALLOWED`, com `Allow` |
| `409` | `IDEMPOTENCY_PAYLOAD_CONFLICT`, `IDEMPOTENCY_KEY_MISMATCH`, `WALLET_ALREADY_EXISTS`, `CONCURRENT_MODIFICATION`, `CONFLICT` |
| `413` | `BODY_TOO_LARGE`, limite de 64 KiB |
| `415` | `UNSUPPORTED_MEDIA_TYPE` |
| `503` | `TEMPORARILY_UNAVAILABLE`, com `Retry-After`: banco, fila ou JWKS indisponíveis |
| `500` | `INTERNAL_ERROR` |

Um `503` numa submissão pode ser reenviado com a mesma `Idempotency-Key`: ou nada foi gravado, ou a resposta será um replay.

## Idempotência

A chave é o header `Idempotency-Key`, escopada por `providerId`. O servidor nunca a substitui por uma calculada. O cliente pode usar `{providerId}:{externalTransactionId}`.

Junto com a chave o serviço persiste um hash SHA-256 do JSON canônico dos campos de negócio, com chaves em ordem alfabética, sem espaços e omitindo `referenceExternalTransactionId` quando vazio:

```
{"amount":"25.00","currency":"BRL","externalTransactionId":"transaction-123","gameId":"fortune-chimp",
 "kind":"BET","playerId":"<uuid>","providerId":"provider-a","roundId":"round-987","walletId":"<uuid>"}
```

Normalizações: `amount` no formato canônico com duas casas, `playerId` e `walletId` como UUID em minúsculas, `kind` em maiúsculas. Chave de idempotência, `messageId`, headers e `occurredAt` ficam fora do hash, então HTTP e SQS produzem o mesmo valor para a mesma operação.

Regras:

- Chave e hash iguais: devolve o resultado persistido com `idempotentReplay: true`, sem tocar a carteira.
- Mesma chave com hash diferente: `409 IDEMPOTENCY_PAYLOAD_CONFLICT`.
- Mesmo `(providerId, externalTransactionId)` com outra chave: `409 IDEMPOTENCY_KEY_MISMATCH`.
- Replays são respondidos sem trancar a carteira. Só uma chave desconhecida toma o lock e reconfere antes de gravar. Índices únicos no banco fecham qualquer corrida restante.

## SQS

`deploy/localstack/init-aws.sh` cria as filas ao subir o LocalStack:

| Fila | Papel | Configuração |
|---|---|---|
| `wager-transactions.fifo` | entrada de operações | visibility 30s, long polling 20s, redrive para a DLQ após 20 recebimentos |
| `wager-transactions-dlq.fifo` | mensagens inválidas ou esgotadas | retenção de 14 dias |
| `wallet-events.fifo` | eventos publicados pelo outbox | retenção de 4 dias |

### Mensagem de entrada

```json
{ "messageId": "msg-123", "type": "WagerTransactionRequested", "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": { "providerId": "provider-a", "externalTransactionId": "transaction-123",
            "idempotencyKey": "provider-a:transaction-123", "playerId": "...", "walletId": "...",
            "roundId": "round-987", "gameId": "fortune-chimp", "kind": "BET",
            "money": { "amount": "25.00", "currency": "BRL" } } }
```

Use o `walletId` como `MessageGroupId` para que operações da mesma carteira cheguem em ordem, e o `messageId` como `MessageDeduplicationId`. O consumidor lê o `MessageGroupId` para não deixar uma mensagem passar na frente de outra do mesmo grupo que falhou.

### Semântica do consumidor

- `messageId` é a identidade durável. A inbox guarda `(consumerName, messageId)` com o hash do corpo; uma reentrega com o mesmo id e corpo é replay, com corpo diferente é veneno.
- Inbox, transação, ledger e outbox são gravados no mesmo commit. A mensagem só é apagada da fila depois do commit.
- Rejeições de negócio são terminais e apagam a mensagem. Corpo inválido, tipo desconhecido, campos inválidos e conflitos de idempotência vão para a DLQ com os atributos `reason`, `originalMessageId` e `receiveCount`, e só então a mensagem original é apagada.
- Falhas transitórias, como banco indisponível, alteram a visibilidade com backoff exponencial de `SQS_RETRY_BACKOFF_BASE` até `SQS_RETRY_BACKOFF_MAX`. Mensagens posteriores do mesmo grupo no lote recebem o mesmo adiamento. A política de redrive move para a DLQ após 20 recebimentos.
- O lote inteiro tem orçamento de 90% do visibility timeout contado da recepção. O que não couber volta à fila com visibilidade zero.
- Em SIGTERM o consumidor para de buscar, termina a mensagem em curso e devolve as demais com visibilidade zero. O prazo total é `APP_STOP_TIMEOUT`.

### Eventos publicados

`wallet-events.fifo` recebe `WagerTransactionProcessed`, `WagerTransactionRejected`, `WagerTransactionPendingReference` e `WalletBalanceChanged`, todos com o envelope:

```json
{ "eventId": "<uuid v7>", "eventType": "WalletBalanceChanged", "aggregateId": "<walletId>",
  "correlationId": "...", "causationId": "<transactionId>", "occurredAt": "...", "version": 1, "data": { } }
```

`MessageGroupId` é o `aggregateId`: a carteira para `WalletBalanceChanged`, a transação para os demais. `MessageDeduplicationId` é o `eventId`, então uma republicação após crash é descartada pela fila. O relay nunca publica um evento enquanto houver outro mais antigo do mesmo agregado por publicar.

## Variáveis de ambiente

| Variável | Padrão | Uso |
|---|---|---|
| `APP_ROLES` | todos | papéis do processo |
| `LOG_LEVEL` | `info` | nível do slog |
| `APP_START_TIMEOUT`, `APP_STOP_TIMEOUT` | `30s`, `25s` | prazos do Fx |
| `HTTP_ADDR` | `:8080` | endereço do servidor |
| `HTTP_READ_TIMEOUT`, `HTTP_WRITE_TIMEOUT`, `HTTP_SHUTDOWN_TIMEOUT` | `10s`, `15s`, `20s` | timeouts do servidor |
| `HTTP_MAX_BODY_BYTES` | `65536` | limite do corpo |
| `DATABASE_URL` | obrigatória | URL pgx, com `pool_max_conns` |
| `DATABASE_CONNECT_TIMEOUT` | `10s` | conexão |
| `OIDC_ISSUER`, `OIDC_AUDIENCE` | obrigatórias | realm e audience |
| `AWS_REGION`, `AWS_ENDPOINT_URL` | `us-east-1`, vazio | SDK; credenciais pelas variáveis padrão da AWS |
| `SQS_WAGER_QUEUE_URL`, `SQS_WAGER_DLQ_URL`, `SQS_EVENTS_QUEUE_URL` | obrigatórias | filas |
| `SQS_CONSUMER_NAME` | `wager-transactions` | nome na inbox |
| `SQS_WAIT_TIME_SECONDS`, `SQS_VISIBILITY_TIMEOUT_SECONDS`, `SQS_MAX_MESSAGES` | `20`, `30`, `10` | recepção |
| `SQS_RETRY_BACKOFF_BASE`, `SQS_RETRY_BACKOFF_MAX` | `2s`, `4m` | retry transitório |
| `OUTBOX_POLL_INTERVAL`, `OUTBOX_BATCH_SIZE`, `OUTBOX_LOCK_TTL` | `500ms`, `50`, `30s` | relay |
| `OUTBOX_BACKOFF_BASE`, `OUTBOX_BACKOFF_MAX` | `1s`, `5m` | retry de publicação |
| `PENDING_REFERENCE_POLL_INTERVAL`, `PENDING_REFERENCE_BATCH_SIZE` | `1s`, `50` | resolvedor |
| `PENDING_REFERENCE_MAX_ATTEMPTS`, `PENDING_REFERENCE_TTL` | `10`, `15m` | quando desistir de uma referência |
| `PENDING_REFERENCE_BACKOFF_BASE`, `PENDING_REFERENCE_BACKOFF_MAX` | `2s`, `2m` | espaçamento das tentativas |
| `FAULT_INJECT` | vazio | `consumer.after_commit_before_ack` ou `outbox.after_publish_before_mark` |

Configuração inválida derruba o processo na partida com todos os erros listados de uma vez.

## Observabilidade

- Logs JSON com `correlationId`, `providerId`, `transactionId` e `roles` em toda linha.
- `GET /health/live` responde sempre `200`. `GET /health/ready` executa os checks de Postgres e SQS em paralelo com 2s de prazo e responde `503` com o detalhe de cada um quando algo está fora.
- `GET /metrics` em formato Prometheus: `wallet_wager_transactions_total{kind,status,code}`, `wallet_idempotent_replays_total{source}`, `wallet_concurrency_conflicts_total{operation}`, `wallet_reconciliations_total{consistent}`, `wallet_messages_handled_total{outcome}`, `wallet_message_processing_duration_seconds{outcome}`, `wallet_outbox_published_total{event_type}`, `wallet_outbox_retries_total{event_type}`, `wallet_outbox_oldest_pending_seconds`, `wallet_outbox_pending_events`, `http_server_requests_total{method,route,status}` e `http_server_request_duration_seconds`, além das métricas de runtime do Go. Os resultados de mensagem são `processed`, `replay`, `rejected`, `pending_reference`, `retry`, `dead_letter`, `in_flight`, `held_back`, `returned` e `ack_failed`.

## Testes

```
make test              # unitários, sem Docker
make lint              # gofmt, vet, staticcheck e govulncheck
make test-integration  # testcontainers: Postgres, Keycloak e LocalStack reais
make e2e               # três réplicas e a suíte caixa-preta por HTTP e SQS
make e2e-fault         # crashes controlados por FAULT_INJECT
```

As suítes de integração e e2e ficam atrás de build tags. Sem `make`:

```
go test -tags integration -timeout 20m ./test/integration/...
docker compose -f docker-compose.yml -f docker-compose.test.yml up --build -d --wait
go test -tags e2e -timeout 15m ./test/e2e/...
APP_ROLES=api,pending docker compose -f docker-compose.yml -f docker-compose.test.yml up --build -d --wait
E2E_FAULT=1 go test -tags e2e -timeout 15m -run Fault ./test/e2e/...
```

A suíte de integração só precisa do Docker rodando: ela mesma sobe e derruba os containers. A e2e espera a stack já de pé e aceita `E2E_BASE_URLS`, `E2E_KEYCLOAK_URL` e `E2E_LOCALSTACK_URL` para apontar para outro lugar.

- Unitários cobrem domínio, casos de uso sobre um fake transacional em memória, router HTTP, verificador OIDC contra um emissor falso, workers com filas falsas e a composição Fx de todas as combinações de papéis.
- Integração sobe os três serviços com testcontainers e prova: migrações sobem, descem e sobem; `wallet_app` não apaga nada e o ledger rejeita mutação até do dono; repositórios e paginação por cursor; claim concorrente do outbox sem duplicar e com ordem por agregado; 50 envios iguais em paralelo com um débito; duas apostas de 80 sobre 100; dois consumidores da mesma mensagem; tokens reais do Keycloak, incluindo expiração e token sem papel; consumidor e relay contra o LocalStack com DLQ; e a aplicação Fx inteira subindo com todos os papéis, atendendo uma chamada autenticada e parando dentro do prazo com o listener fechado.
- E2E roda contra o compose de teste com três réplicas: readiness, ciclo da carteira entre réplicas, 50 envios iguais distribuídos, 80 mais 80 sobre 100, contrato de idempotência, reversão antes da referência, mesma operação por SQS e HTTP, mensagem envenenada na DLQ e ordem dos eventos publicados.
- `make e2e-fault` sobe as réplicas sem os papéis `consumer` e `outbox`, roda o container `app-fault` com `FAULT_INJECT` e prova que o crash depois do commit e antes do ack não debita duas vezes, que o crash depois de publicar e antes de marcar não duplica o evento, e que um `docker compose restart` das réplicas preserva idempotência, pendências e consistência. O processo sai com código 3 quando a falha injetada dispara.

`go test -race` exige cgo e roda em Linux ou macOS. No Windows sem toolchain C use o `make test` normal.

## Limitações conhecidas

- `/metrics` não exige autenticação. Em produção fica atrás do ingress ou em porta separada.
- O outbox nunca descarta um evento que falha ao publicar: o agregado fica represado e `wallet_outbox_oldest_pending_seconds` denuncia. Pular o evento quebraria a ordem.
- Não há retenção de `outbox_events` e `inbox_messages`; um job com o papel `wallet_migrator` deve apagar publicados e concluídos antigos.
- O LocalStack Community não aplica as políticas IAM criadas no init; elas documentam o que cada credencial precisaria na AWS.
- Os secrets do Keycloak e do banco são fixos no compose para desenvolvimento. Em produção vêm de um cofre.
- A existência de uma carteira é observável por quem tem token de provedor via `WALLET_PLAYER_MISMATCH`.
