export default {
  async fetch(request, env) {
    const path = new URL(request.url).pathname;
    if (path === "/database") {
      return Response.json(await env.DB.prepare("SELECT 1 AS ready").first());
    }
    if (path === "/counter") {
      return env.COUNTER.get(env.COUNTER.idFromName("example")).fetch(request);
    }
    if (path.startsWith("/assets/")) {
      const object = await env.ASSETS.get(path.slice("/assets/".length));
      return object ? new Response(object.body) : new Response("Not found", { status: 404 });
    }
    return Response.json({ ready: true });
  },
};

export class Counter {
  constructor(state) {
    this.state = state;
  }

  async fetch() {
    const count = (await this.state.storage.get("count") ?? 0) + 1;
    await this.state.storage.put("count", count);
    return Response.json({ count });
  }
}
