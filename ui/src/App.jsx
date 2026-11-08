import { Navigate, Route, Routes } from "react-router-dom";
import { getToken } from "./api.js";
import Login from "./pages/Login.jsx";
import Register from "./pages/Register.jsx";
import Workspace from "./pages/Workspace.jsx";

function Private({ children }) {
  if (!getToken()) return <Navigate to="/login" replace />;
  return children;
}

function Home() {
  return <Navigate to={getToken() ? "/app" : "/login"} replace />;
}

export default function App() {
  return (
    <Routes>
      <Route path="/" element={<Home />} />
      <Route path="/login" element={<Login />} />
      <Route path="/register" element={<Register />} />
      <Route
        path="/app/*"
        element={
          <Private>
            <Workspace />
          </Private>
        }
      />
    </Routes>
  );
}
