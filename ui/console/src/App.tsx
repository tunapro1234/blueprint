import { Navigate, Route, Routes } from "react-router-dom";

import { AppShell } from "./components/AppShell";
import { OrgPage } from "./pages/OrgPage";
import { TasksPage } from "./pages/TasksPage";
import { UsagePage } from "./pages/UsagePage";
import { WorkflowsPage } from "./pages/WorkflowsPage";
import { WorkflowDetailPage } from "./pages/WorkflowDetailPage";

export function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<Navigate to="/org" replace />} />
        <Route path="/org" element={<OrgPage />} />
        <Route path="/tasks" element={<TasksPage />} />
        <Route path="/usage" element={<UsagePage />} />
        <Route path="/workflows" element={<WorkflowsPage />} />
        <Route path="/workflows/:name" element={<WorkflowDetailPage />} />
        <Route path="*" element={<Navigate to="/org" replace />} />
      </Route>
    </Routes>
  );
}
