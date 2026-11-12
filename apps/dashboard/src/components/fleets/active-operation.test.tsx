import { act, fireEvent, render, screen } from "@testing-library/react"
import { beforeEach, expect, it, vi } from "vitest"
import { encode } from "@/client/forge/loomplane/codecs"
import { ops, type OperationMeta } from "@/client/forge/loomplane/ops"
import { ActiveOperation } from "./active-operation"

it("encodes the reviewed revision through the generated cancellation contract", () => {
  const cancel: OperationMeta = ops["fleetOperations.cancel"]
  expect(cancel.bodyCodec).toBeDefined()
  expect(encode({ expectedInstanceRevision: 7 }, cancel.bodyCodec)).toEqual({
    expected_instance_revision: 7,
  })
})

it("encodes action pins and nested move fields through the generated contract", () => {
  const action: OperationMeta = ops["fleetInstances.requestAction"]
  expect(action.bodyCodec).toBeDefined()
  expect(
    encode(
      {
        kind: "move",
        idempotencyKey: "reviewed-move",
        expectedRevision: 7,
        expectedGeneration: 3,
        move: { targetCloudId: "cloud-2" },
      },
      action.bodyCodec
    )
  ).toEqual({
    kind: "move",
    idempotency_key: "reviewed-move",
    expected_revision: 7,
    expected_generation: 3,
    move: { target_cloud_id: "cloud-2" },
  })
})

const state = vi.hoisted(() => ({
  can: true,
  query: {
    status: "success",
    data: undefined as unknown,
    error: undefined as unknown,
    isFetching: false,
    refetch: vi.fn<() => Promise<unknown>>(async () => ({})),
  },
  mutation: {
    isPending: false,
    mutateAsync: vi.fn<(args: unknown) => Promise<unknown>>(async () => ({})),
  },
  queryArgs: undefined as unknown,
}))
vi.mock("@/client/forge/loomplane/hooks", () => ({
  useFleetInstancesActiveOperation: "active",
  useFleetOperationsCancel: "cancel",
}))
vi.mock("@/client/hooks", () => ({
  useQuery: (_hook: unknown, args: unknown) => {
    state.queryArgs = args
    return state.query
  },
  useMutation: () => state.mutation,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (permission: string) =>
      permission === "deployment:create" && state.can,
  }),
}))
const operation = {
  id: "operation-1",
  instanceId: "instance-1",
  kind: "create",
  status: "waiting_approval",
  generation: 2,
  createdAt: "2026-11-01T12:00:00Z",
}
const instance = {
  id: "instance-1",
  revision: 3,
  desiredGeneration: 2,
  status: "waiting_approval",
}
const changed = vi.fn<() => Promise<unknown>>(async () => ({}))
const props = { projectID: "project", instance, onChanged: changed }

beforeEach(() => {
  state.can = true
  state.query = {
    status: "success",
    data: { ...operation },
    error: undefined,
    isFetching: false,
    refetch: vi.fn(async () => ({})),
  }
  state.mutation = {
    isPending: false,
    mutateAsync: vi.fn(async () => ({ ...operation, status: "cancelled" })),
  }
  changed.mockClear()
  changed.mockResolvedValue({})
})
it("reads the instance-scoped active operation and explains server cancellation checks", () => {
  render(<ActiveOperation {...props} />)
  expect(state.queryArgs).toEqual({
    path: { project_id: "project", id: "instance-1" },
  })
  expect(screen.getByText("Operation operation-1")).toBeInTheDocument()
  expect(screen.getByText("Waiting approval")).toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Request cancellation" })
  ).toBeEnabled()
  expect(screen.getByText(/server rechecks admission/i)).toBeInTheDocument()
})
it.each([
  ["other instance", { instanceId: "instance-2" }],
  ["earlier generation", { generation: 1 }],
  ["later generation", { generation: 3 }],
  ["update operation", { kind: "update" }],
  ["queued operation", { status: "queued" }],
  ["running operation", { status: "running" }],
])("does not expose cancellation for %s", (_name, changes) => {
  state.query.data = { ...operation, ...changes }
  render(<ActiveOperation {...props} />)
  expect(
    screen.queryByRole("button", { name: "Request cancellation" })
  ).not.toBeInTheDocument()
})
it("requires deployment permission and a waiting instance", () => {
  state.can = false
  const view = render(<ActiveOperation {...props} />)
  expect(
    screen.queryByRole("button", { name: "Request cancellation" })
  ).not.toBeInTheDocument()
  state.can = true
  view.rerender(
    <ActiveOperation
      {...props}
      instance={{ ...instance, status: "provisioning" }}
    />
  )
  expect(
    screen.queryByRole("button", { name: "Request cancellation" })
  ).not.toBeInTheDocument()
})
it("disables cancellation while a read or mutation is pending", () => {
  state.query.isFetching = true
  const view = render(<ActiveOperation {...props} />)
  expect(
    screen.getByRole("button", { name: "Request cancellation" })
  ).toBeDisabled()
  state.query.isFetching = false
  state.mutation.isPending = true
  view.rerender(<ActiveOperation {...props} />)
  expect(
    screen.getByRole("button", { name: "Request cancellation" })
  ).toBeDisabled()
})
it("distinguishes initial loading from no active operation", () => {
  state.query.data = undefined
  state.query.status = "loading"
  render(<ActiveOperation {...props} />)
  expect(screen.getByRole("status")).toHaveTextContent(
    "Loading active operation"
  )
  expect(screen.queryByText("No active operation")).not.toBeInTheDocument()
})
it("treats a scoped404 as no active operation using the shared empty state", () => {
  state.query.status = "error"
  state.query.error = { statusCode: 404 }
  render(<ActiveOperation {...props} />)
  expect(
    screen.getByText("No active operation").closest('[data-slot="empty"]')
  ).toHaveAttribute("data-variant", "illustrated")
  expect(screen.queryByText("Operation operation-1")).not.toBeInTheDocument()
  expect(
    screen.getByRole("button", { name: "Refresh operation" })
  ).toBeInTheDocument()
})
it.each([401, 403, 500, 503])(
  "hides cached operation after read status%s",
  (statusCode) => {
    state.query.status = "error"
    state.query.error = { statusCode, message: "private-token" }
    render(<ActiveOperation {...props} />)
    expect(screen.queryByText("Operation operation-1")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Request cancellation" })
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/private-token/)).not.toBeInTheDocument()
    expect(
      screen.getByText(
        statusCode === 401 || statusCode === 403
          ? "Operation access denied"
          : "Could not load the active operation"
      )
    ).toBeInTheDocument()
  }
)
it("submits the operation identity and instance revision once, then reloads both reads", async () => {
  render(<ActiveOperation {...props} />)
  await act(async () => {
    fireEvent.click(
      screen.getByRole("button", { name: "Request cancellation" })
    )
  })
  expect(state.mutation.mutateAsync).toHaveBeenCalledExactlyOnceWith({
    path: { project_id: "project", id: "operation-1" },
    body: { expectedInstanceRevision: 3 },
  })
  expect(changed).toHaveBeenCalledOnce()
  expect(state.query.refetch).toHaveBeenCalledOnce()
  expect(screen.getByRole("status")).toHaveTextContent(
    "Cancellation request accepted"
  )
})
it("keeps captured identity and revision stable while props change and prevents double submission", async () => {
  let finish = () => {}
  state.mutation.mutateAsync.mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = () => resolve({ ...operation, status: "cancelled" })
      })
  )
  const view = render(<ActiveOperation {...props} />)
  const button = screen.getByRole("button", { name: "Request cancellation" })
  fireEvent.click(button)
  fireEvent.click(button)
  state.query.data = { ...operation, id: "operation-2", generation: 3 }
  view.rerender(
    <ActiveOperation
      {...props}
      instance={{ ...instance, revision: 4, desiredGeneration: 3 }}
    />
  )
  expect(state.mutation.mutateAsync).toHaveBeenCalledExactlyOnceWith({
    path: { project_id: "project", id: "operation-1" },
    body: { expectedInstanceRevision: 3 },
  })
  await act(async () => {
    finish()
  })
})
it.each([
  [
    409,
    "The instance or operation changed. Reload both before requesting cancellation again.",
  ],
  [403, "You do not have permission to cancel this operation."],
  [
    500,
    "Could not confirm cancellation. Reload the current state before deciding whether to try again.",
  ],
])(
  "redacts mutation status%s and only retries reads",
  async (statusCode, message) => {
    state.mutation.mutateAsync.mockRejectedValue({
      statusCode,
      message: "private-token",
    })
    render(<ActiveOperation {...props} />)
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: "Request cancellation" })
      )
    })
    expect(screen.getByRole("alert")).toHaveTextContent(message)
    expect(screen.queryByText(/private-token/)).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Request cancellation" })
    ).not.toBeInTheDocument()
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: "Reload current state" })
      )
    })
    expect(changed).toHaveBeenCalledOnce()
    expect(state.query.refetch).toHaveBeenCalledOnce()
    expect(state.mutation.mutateAsync).toHaveBeenCalledOnce()
  }
)
it("does not report a reload failure as a failed cancellation", async () => {
  changed.mockRejectedValue({ statusCode: 500, message: "private-token" })
  render(<ActiveOperation {...props} />)
  await act(async () => {
    fireEvent.click(
      screen.getByRole("button", { name: "Request cancellation" })
    )
  })
  expect(screen.getByRole("alert")).toHaveTextContent(
    "Cancellation request accepted, but the current state could not be refreshed."
  )
  expect(screen.queryByText(/private-token/)).not.toBeInTheDocument()
  expect(state.mutation.mutateAsync).toHaveBeenCalledOnce()
})
it("accepts404 from the follow-up operation read after cancellation", async () => {
  state.query.refetch.mockRejectedValue({ statusCode: 404 })
  render(<ActiveOperation {...props} />)
  await act(async () => {
    fireEvent.click(
      screen.getByRole("button", { name: "Request cancellation" })
    )
  })
  expect(screen.getByRole("status")).toHaveTextContent(
    "Cancellation request accepted"
  )
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
})
