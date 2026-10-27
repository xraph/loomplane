# Loomplane Client

Loomplane's API: organizations' projects, environments and infrastructure.

Generated client for Loomplane 0.1.0-dev

## Installation

```bash
npm install @loomplane/loomplane-client
# or
yarn add @loomplane/loomplane-client
```

## Usage

```typescript
import { Client } from '@loomplane/loomplane-client';

const client = new Client({
  auth: {
    bearerToken: 'your-token-here',
  },
});

// Use the client
// const result = await client.someMethod(...);
```

## Authentication

This API supports the following authentication methods:

### session

- **Type**: http
- **Scheme**: bearer
- **Usage**: Pass the token in the `Authorization` header as `Bearer <token>`

### bearerAuth

- **Type**: http
- **Scheme**: bearer
- **Bearer Format**: JWT
- **Usage**: Pass the token in the `Authorization` header as `Bearer <token>`

### session-cookie

- **Type**: apiKey
- **Location**: cookie
- **Cookie Name**: auth_token


## API Overview

- **Total Endpoints**: 227
- **REST Endpoints**: 225
- **WebSocket Endpoints**: 1
- **SSE Endpoints**: 1

### Servers

- **http://localhost:8780**: Development server

