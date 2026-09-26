# Arquitetura

## Camadas

Arquitetura hexagonal com um domínio de DDD. As dependências apontam sempre para dentro: adaptadores conhecem a aplicação, a aplicação conhece o domínio, o domínio não conhece ninguém.

```
cmd/
  server        lê APP_ROLES, monta o grafo Fx e controla start, wait e stop
  migrate       golang-migrate com as migrações embutidas
  healthcheck   binário mínimo para o HEALTHCHECK da imagem distroless
internal/
  domain/       money, wallet, wager, event: regras puras, sem I/O
  application/  portas, casos de uso, erros e o fake transacional apptest
    wallets     abrir, consultar, ledger e reconciliação
    wagering    submissão, consumo por mensagem, pendências, fingerprint
  adapters/
    postgres    unit of work sobre pgx, repositórios, tradução de erros
    httpapi     chi, OIDC, DTOs, tabela de erros
    awssqs      fila de entrada, DLQ e publicador de eventos
    telemetry   logger, relógio e métricas Prometheus
  worker/       consumer, outbox relay, pending resolver, loop e fault injection
  config/       leitura e validação do ambiente
  bootstrap/    composição dos módulos por papel
migrations/     SQL versionado, embutido no binário
deploy/         scripts de init do Postgres e do LocalStack, realm do Keycloak
test/           testutil, integration e e2e, atrás de build tags
```

`application` não importa pgx, chi, AWS nem Fx. Adaptadores não importam uns aos outros; quem liga telemetria ao HTTP é o bootstrap.

## Domínio

**Money** é `int64` em unidades menores com moeda de uma lista fechada. O intervalo é simétrico, com `MinInt64` excluído, para que negar e subtrair nunca estourem sem aviso: o maior valor representável é 92.233.720.368.547.758,07 e o menor é o seu negativo. Soma, subtração e parsing detectam overflow e devolvem erro. Parse aceita apenas a forma canônica `^(0|[1-9]\d*)\.\d{2}$`: sem sinal, sem espaços, sem zeros à esquerda, sem notação científica e sem escala diferente de duas casas; nenhuma forma equivalente é normalizada antes do hash de idempotência, então `"25.0"` é rejeitado em vez de virar `"25.00"`. Nada de float em lugar nenhum. No banco o valor vai como `BIGINT` em unidades menores e a moeda como `TEXT` com CHECK de três letras maiúsculas.

**Wallet** é o agregado de saldo. `Open` cria a carteira na versão 1 e, se o saldo inicial é positivo, o lançamento de crédito. `Apply` valida o movimento, calcula o saldo, cria o `LedgerEntry` com saldo anterior e posterior, incrementa a versão e atualiza `updatedAt`. Um relógio anterior ao último `updatedAt` é ajustado para ele: o agregado nunca anda para trás.

**Wager** é a transação. Máquina de estados `PENDING → PENDING_REFERENCE | PROCESSED | REJECTED | FAILED`, com `PENDING_REFERENCE` podendo ir para qualquer terminal. `OPENING` nasce `PROCESSED`. Cada mutação valida tudo antes de tocar o estado; `Rehydrate` recusa qualquer combinação impossível de tipo, status, código, saldo e datas, então o banco nunca devolve uma transação que o domínio não reconheça. `rules.Evaluate` produz um veredito: prosseguir com o movimento e a direção, aguardar a referência, ou rejeitar com código.

Regras de referência: `REFUND` reverte `BET`; `ROLLBACK` reverte `BET`, `WIN` ou `REFUND` na direção oposta; `WIN` pode citar um `BET`. Referência com outro provedor, rodada, jogador ou carteira é `REFERENCE_MISMATCH`. Reversão exige o mesmo valor. Cada transação aceita uma única reversão bem-sucedida, de qualquer tipo, garantida também por índice único parcial.

Combinações sobre a mesma aposta: depois de um `REFUND` processado, um `ROLLBACK` da mesma `BET` é `REFERENCE_ALREADY_REVERSED`, porque o débito já foi devolvido. O caminho correto para desfazer o reembolso é um `ROLLBACK` que referencia o próprio `REFUND`, que debita o valor de volta; a partir daí a `BET` volta a estar líquida e não aceita nova reversão. Um `ROLLBACK` de `WIN` debita, e se o saldo não cobre o débito a rejeição é `REVERSAL_INSUFFICIENT_FUNDS`, distinta de uma aposta sem saldo.

Referência existente mas não terminal, ainda `PENDING` ou `PENDING_REFERENCE`: a operação aguarda como `PENDING_REFERENCE`, igual a uma referência ausente. Referência terminada como `REJECTED` ou `FAILED`: rejeição imediata com `REFERENCE_NOT_PROCESSED`. Referência ausente até esgotar `MaxAttempts` ou `TTL`: `REFERENCE_NOT_FOUND`, com o evento de rejeição.

Falhas transitórias e permanentes: erros de infraestrutura, como banco ou fila indisponíveis, timeouts e cancelamentos, nunca mudam o estado da transação; a operação é retentada pelo cliente, pela fila ou pelo resolvedor. Erros de integridade, entrada inválida ou referência inexistente durante a resolução de uma pendência são permanentes: a transação vai para `FAILED` com `PERMANENT_FAILURE`, em transação separada, para auditoria e para não bloquear a fila de pendências. Rejeições de negócio não são falhas: são resultados `REJECTED` com código estável.

**Event** é o único pacote do domínio com JSON. Os construtores exigem o estado que descrevem: `NewWalletBalanceChanged` só aceita o lançamento mais recente da carteira. `Decode` é estrito: campos desconhecidos, `data` nulo e lixo após o JSON são erro.

## Aplicação

`UnitOfWork.Do(ctx, opts, fn)` abre uma transação e entrega os repositórios. Tudo o que um caso de uso grava acontece dentro de um único `Do`.

### Submissão

1. Valida o comando e os campos externos sem tocar o banco.
2. Consulta a chave de idempotência sem lock. Encontrou: confere o hash e responde o resultado persistido. É o caminho dos 49 reenvios.
3. `SELECT ... FOR UPDATE` na carteira. A partir daqui só uma instância mexe nela.
4. Reconfere chave e `(providerId, externalTransactionId)` sob o lock, porque outra instância pode ter commitado no meio.
5. Lê o relógio e o empurra para depois do `updatedAt` da carteira. Assim o `createdAt` da transação, o lançamento e o `occurredAt` dos eventos seguem a ordem do lock, não do relógio de cada instância.
6. Carrega a referência, avalia, e conclui: aguarda, rejeita ou processa. Processar aplica o movimento, grava a transação, a carteira com verificação de versão, o lançamento e os eventos no outbox.
7. Métricas só depois do commit.

### Pendências

Uma reversão cuja referência não chegou fica `PENDING_REFERENCE` com `next_attempt_at`. O resolvedor toma a linha com `FOR UPDATE SKIP LOCKED`, tranca a carteira e reavalia. Backoff exponencial entre tentativas; `MaxAttempts` ou `TTL` esgotados rejeitam com `REFERENCE_NOT_FOUND`. Erro de integridade durante a resolução marca `FAILED` em transação separada, para que a linha não bloqueie as demais.

### Consumo por mensagem

`Consume` lê a inbox pelo `messageId`. Concluída com o mesmo hash: replay. Hash diferente: conflito, mensagem envenenada. Inexistente: insere a linha e segue para a mesma submissão do HTTP; inbox, transação e outbox commitam juntos. Se dois consumidores recebem a mesma mensagem, o segundo bloqueia no índice único da inbox até o primeiro commitar e então recebe `ErrMessageInFlight`.

## Persistência

Biblioteca: pgx/v5 com SQL explícito, sem ORM e sem gerador. Cada query fica visível no repositório, com placeholders posicionais e leitura por nome de coluna, então locks, constraints e planos são verificáveis.

Delimitação da transação: `UnitOfWork.Do` abre uma transação pgx e constrói os cinco repositórios sobre o mesmo `pgx.Tx`; os repositórios nunca abrem transações próprias nem recebem o pool. Um caso de uso é exatamente um `Do`: commit no retorno nulo, rollback em erro ou pânico. Leituras usam `ReadOnly`. Nada de transações aninhadas.

Mapeamento de Money: `balance_units` e `amount_units` em `BIGINT`, `currency` em `TEXT`; saldo anterior e posterior também em `BIGINT`. A reidratação recompõe o value object e recusa moeda ou valor fora do domínio.

Cinco migrações: `wallets`, `wager_transactions`, `wallet_ledger_entries`, `inbox_messages`, `outbox_events`. As constraints espelham as invariantes do domínio: status e tipo conhecidos, estados terminais com `completed_at`, resultado só em status terminal, saldo não negativo, lançamento cujo `balance_after = balance_before ± amount`, um `OPENING` por carteira, chave de idempotência única por provedor, uma reversão processada por referência.

O ledger é imutável por três mecanismos: trigger de linha que rejeita `UPDATE` e `DELETE`, trigger de `TRUNCATE`, e `REVOKE` para o papel da aplicação. `wallet_app` não tem `DELETE` em nenhuma tabela nem escrita em `schema_migrations`; `wallet_migrator` é dono do schema e roda as migrações.

O pool define `statement_timeout`, `lock_timeout` e `idle_in_transaction_session_timeout`, então uma sessão presa não esgota as conexões das três instâncias. Rollback usa contexto próprio para não destruir a conexão quando o cliente desiste. Erros do Postgres viram sentinelas da aplicação: violação de unicidade é `ConflictError` com o nome da constraint, outras integridades são `ErrIntegrity`, códigos transitórios e falhas de rede são `ErrUnavailable`.

## Concorrência entre instâncias

- Lock pessimista por carteira: a linha da carteira é a unidade de serialização. Duas apostas de 80 sobre 100 são avaliadas uma depois da outra, e a segunda vê 20.
- Versão otimista no `UPDATE` da carteira como cinto e suspensório.
- Replays não tomam lock. Chaves desconhecidas tomam e reconferem; os índices únicos decidem a última corrida possível.
- Timestamps por agregado são monotônicos, o que dá ao outbox uma ordem por agregado independente de relógio.
- Filas de trabalho no banco, outbox e pendências, usam `FOR UPDATE SKIP LOCKED` com lease e dono, então N instâncias dividem o trabalho sem coordenação externa.

## Mensageria

### Consumer

Recebe até 10 mensagens com long polling e as trata em sequência dentro de um orçamento de 90% do visibility timeout contado da recepção. Resultados: `processed`, `replay`, `rejected` e `pending_reference` apagam a mensagem depois do commit; `dead_letter` copia para a DLQ e apaga; `retry` adia a visibilidade com backoff; `in_flight` deixa a mensagem para outra instância; `held_back` adia as mensagens seguintes do mesmo grupo quando uma anterior falhou; `returned` devolve o que não coube no orçamento ou o que sobrou no shutdown com visibilidade zero.

### Outbox relay

Claim em uma única query: seleciona eventos devidos e sem lease, ignora qualquer evento que tenha outro mais antigo não publicado do mesmo agregado, ordena por `(occurred_at, event_id)` e marca `locked_by` e `locked_until`. Publica cada evento com timeout próprio, confere o lease antes de enviar, marca como publicado com o mesmo dono do lease e, em falha, libera com backoff. No shutdown, os eventos reivindicados e não publicados são liberados na hora. Um crash entre publicar e marcar republica o evento com o mesmo `eventId`, que a fila FIFO descarta.

### Fault injection

`FAULT_INJECT` aceita dois pontos: depois do commit do consumidor e antes do ack, e depois da publicação do relay e antes da marcação. O processo sai com código 3. O valor é validado na partida e só é ligado no serviço `app-fault` do compose de teste, nunca no ambiente compartilhado das réplicas.

## Autenticação e autorização

IdP: Keycloak, por rodar em um container com realm importado de arquivo, oferecer `client_credentials` para comunicação entre serviços, papéis de realm e mappers de claim sem código extra, e por ser o que a maioria dos ambientes corporativos já opera. O realm em `deploy/keycloak` provisiona clients, service accounts, papéis e a claim `providerId`, então um checkout limpo sobe autenticado.

Tokens são verificados com go-oidc contra o discovery do realm: assinatura, emissor, audience `wallet-api` e expiração com 30s de tolerância. O papel `wallet-internal` vira principal interno; `wallet-provider` exige a claim `providerId`. Falha ao buscar as chaves do JWKS é `503`, não `401`, para que o provedor não conclua que sua credencial quebrou.

A autorização fica na aplicação: abrir e ler carteiras exige interno; submeter exige o `providerId` do token igual ao do corpo; consultas de transação por provedor só enxergam o próprio provedor, e o resto responde `404` para não revelar existência.

## Fx e ciclo de vida

`bootstrap.New(roles)` compõe config, telemetria, Postgres, serviços e HTTP em toda instância, SQS quando há `consumer` ou `outbox`, e cada worker apenas com o seu papel. `fx.ValidateApp` roda sobre as quinze combinações de papéis nos testes.

Partida: pool e ping do Postgres, discovery OIDC com retry até o prazo, servidor HTTP, workers. Parada em ordem inversa: workers param de buscar e terminam o que estava em curso, o servidor drena conexões, o pool fecha. `APP_STOP_TIMEOUT` limita o total; um segundo sinal aborta.

## Observabilidade

slog em JSON com `roles` em toda linha e `correlationId` por requisição e mensagem. Métricas em registry privado do Prometheus, com labels limitados a valores conhecidos para não explodir cardinalidade. Readiness executa os checks em paralelo com prazo curto.

## Interpretações adotadas

- Operações sem dependência são concluídas de forma síncrona dentro de uma única transação; não há commit intermediário de aceite. Só reversões sem referência ficam `PENDING_REFERENCE`, e essa é a única pendência durável retomada por outra instância.
- `WIN` com `referenceExternalTransactionId` valida a referência quando ela existe e aguarda quando não existe, como as reversões; a referência é opcional apenas na entrada.
- `LOSS` exige `"0.00"`, produz `WagerTransactionProcessed` e não toca saldo, ledger nem versão.
- Uma referência aceita uma única reversão bem-sucedida de qualquer tipo, mais restrito do que "do mesmo tipo", para que um débito nunca seja devolvido duas vezes por caminhos diferentes.
- A chave de idempotência é escopada por provedor: dois provedores podem usar a mesma string sem colidir.
- O `providerId` de uma mensagem SQS é aceito como vem, porque a fila é interna e protegida pelas credenciais do broker; as validações de domínio continuam sendo aplicadas.
- `WALLET_PLAYER_MISMATCH` revela que a carteira existe para quem tem token de provedor. Preferimos o código útil ao provedor a esconder a existência.

## Trade-offs, limitações e trabalho não concluído

- Ordem por agregado no outbox depende de timestamps monotônicos por agregado, garantidos pelo lock da carteira. Uma sequência por agregado no banco seria uma alternativa mais explícita.
- Eventos que falham ao publicar nunca são descartados; o agregado fica represado e a métrica de idade do outbox alerta.
- O fake em memória usado nos testes unitários serializa transações; a semântica real de locks e índices é coberta pelos testes de integração com Postgres.
- Sem retenção de outbox e inbox, sem partições, sem retry automático de erros de serialização, porque nenhuma transação usa `SERIALIZABLE`.
- `/metrics` sem autenticação e secrets fixos no compose são posturas de desenvolvimento.
- O LocalStack Community não aplica as políticas IAM criadas no init; elas descrevem o que cada credencial precisaria na AWS.
- Não há tracing com OpenTelemetry, dashboards nem teste de carga; partidas dobradas no ledger não foram implementadas.
- `go test -race` exige cgo e foi validado apenas em Linux; a suíte de integração e a e2e dependem de Docker e não rodam em CI sem ele.
