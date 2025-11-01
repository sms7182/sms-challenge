SMS Delivery System
Overview
This service provides an API-based SMS delivery system that handles both normal and express message types.
It manages message queuing, credit balance checking, and delivery tracking, all in a distributed and safe way.
The main goal is to ensure:
Each client can only send messages within their available credit.
Message delivery is handled asynchronously through Redis queues.
Express messages meet stricter delivery guarantees (SLA).
Architecture
Components
Component	Responsibility
REST API (Gin)	Handles client requests for sending messages, increasing balance, and fetching delivery reports.
Redis	Used for atomic credit operations and as a message queue for workers.
PostgreSQL	Stores messages, delivery logs, and client credit history.
Workers	Background processes that consume Redis queues and simulate message delivery.
Data Flow
Message Request
The client sends a bulk SMS request via POST /api/messages/bulk/send.
The X-Client-ID header identifies the client.
The X-SMS-TYPE header determines whether it’s normal or express.
Atomic Credit Check (Lua Script)
A Lua script in Redis checks and deducts available credits atomically.
If the request exceeds the balance, only part of the batch is accepted.
When the balance reaches zero, the key is deleted from Redis.
Queueing
Accepted messages are pushed to the Redis list:
sms:normal for standard messages
sms:express for time-sensitive ones
Workers
Two independent goroutines run as workers:
startNormalWorker() consumes from sms:normal
startExpressWorker() consumes from sms:express
Each worker pops messages, sends them (simulated), and updates the database.
If sending fails, the credit is refunded atomically using another Lua script.
Reporting
The endpoint GET /api/messages/sent-log returns message logs for each client with optional filters (status, type, date).
Pagination is supported.
Database Schema
Table: messages
Field	Type	Description
id	UUID	Unique identifier
client_id	String	Client reference
type	String	Message type (normal, express)
to_number	String	Recipient
from_number	String	Sender
content	Text	Message body
status	String	queued, sent, or failed
queued_at	Timestamp	When the message was queued
updated_at	Timestamp	Last update time
error_message	Text (nullable)	Delivery error message
Table: credits
Field	Type	Description
client_id	String	Client reference
name	String	Client name
balance	Integer	Remaining credits
updated_at	Timestamp	Last update time
Endpoints
1. Increase Credit
POST /api/credit/increase
Headers:
  X-Client-ID: <client_id>
Body:
{
  "name": "Mojtaba",
  "balance": 50
}
2. Send Bulk Messages
POST /api/messages/bulk/send
Headers:
  X-Client-ID: <client_id>
  X-SMS-TYPE: express
Body:
{
  "messages": [
    {"to": "+4917012345", "from": "App", "content": "Your OTP code is 1234"},
    {"to": "+4917098765", "from": "App", "content": "Welcome to the system"}
  ]
}
3. Fetch Message Logs
GET /api/messages/sent-log?status=sent&type=express&page=1&page_size=10
Headers:
  X-Client-ID: <client_id>
Credit Management Logic
All credit updates happen in Redis using Lua scripts to avoid race conditions.
Operations are atomic even under concurrent requests.
If a queue push or DB write fails, the credit is automatically refunded.
PostgreSQL is updated asynchronously in workers to ensure persistence.
Workers
Worker	Queue	Description
Normal Worker	sms:normal	Handles regular message traffic without SLA.
Express Worker	sms:express	Handles time-sensitive messages (e.g., OTP).
Each worker runs as a separate goroutine and processes messages indefinitely.
Key Technical Notes
GORM is used for ORM with PostgreSQL and is fully thread-safe.
Redis Lua scripts ensure atomicity in credit operations.
Context management is used for request and worker lifecycle control.
Refund logic prevents data inconsistency between Redis and the database.
Future Improvements
Add JWT-based authentication instead of plain X-Client-ID.
Implement retry logic for failed messages.
Add Prometheus metrics for SLA monitoring.
Add a dashboard to monitor Redis queues and message throughput.
Cache message reports for better performance.
